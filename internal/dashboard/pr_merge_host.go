package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
)

// handlePRMergeHostWS runs a "Merge with host" job: it prepares a THROWAWAY
// host checkout of the PR's branch and runs one host-`claude` turn (act-capable
// — Bash/Edit, so it can rebase, resolve conflicts, push, and `gh pr merge`)
// with the editable pr.merge prompt, streaming the session to the browser over
// the same transport the chat panels use. When the socket closes the checkout
// is removed.
//
//	GET /prs/<prId>/merge-host/ws
//
// NOT SANDBOXED: this runs the operator's own host `claude` with Bash against a
// real git checkout, using host git/gh credentials. It's the fast path (no
// container spin-up); the UI warns that it isn't sandboxed. The prompt is
// auto-submitted as the first (and typically only) turn; the user can keep
// chatting to steer conflict resolution.
func (d *dashboardServer) handlePRMergeHostWS(w http.ResponseWriter, r *http.Request, prID int64) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}

	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)

	repoID, err := svc.RepoIDForPR(prID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ownerName := d.ownerNameForPR(svc, prID)
	if ownerName == "" {
		http.Error(w, "repo is not a GitHub remote", http.StatusBadRequest)
		return
	}
	mi, err := svc.PRMergeInfo(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(mi.HeadRef) == "" {
		http.Error(w, "PR has no known branch — refresh it from GitHub first", http.StatusBadRequest)
		return
	}

	repoName, defaultBranch := ownerName, "main"
	if repo, gerr := repos.Get(repoID); gerr == nil {
		if repo.Name != "" {
			repoName = repo.Name
		}
		if repo.DefaultBranch != "" {
			defaultBranch = repo.DefaultBranch
		}
	}
	allowed, preferred := d.resolveRepoMergeMethods(repoID, ownerName)
	strategy := effectiveMergeStrategy(preferred, allowed)
	mergePrompt := d.renderMergePrompt(repoID, prPromptSlots(repoName, mi, strategy, defaultBranch))

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(m chatServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(m)
	}

	// Prepare the throwaway host checkout on the PR branch. A local clone from the
	// repo mirror (which CloneLocal repoints at the real remote), so push / gh
	// merge use the host's ambient git/gh auth.
	checkout := hostMergeCheckoutPath(prID)
	_ = os.RemoveAll(checkout) // clear any stale dir from a previous run
	_ = send(chatServerMsg{Type: "text", Text: fmt.Sprintf("Preparing a host checkout of %s on %s…\n", repoName, mi.HeadRef)})
	if err := repos.CloneLocal(repoID, checkout, mi.HeadRef); err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "failed to prepare host checkout: " + err.Error()})
		return
	}
	// Remove the checkout when the session ends (socket closed / turn done).
	defer os.RemoveAll(checkout)

	d.applog().Log(applog.Entry{
		Category: applog.CatPRAction, Event: "pr.merge_host.start",
		Message: applog.Fmt("Merge-with-host started for %s#%d (%s) in a host checkout", ownerName, mi.Number, strategy),
		RepoID:  repoID, Status: applog.StatusOK,
		Meta: map[string]any{"owner": ownerName, "pr": mi.Number, "strategy": strategy},
	})

	// Act-capable tools: the whole point is to run git/gh. Grant the standard
	// working set; the host `claude` still honors the operator's own permissions.
	tools := []string{"Bash", "Read", "Edit", "Write", "Grep", "Glob"}

	var turnMu sync.Mutex
	var turnCancel context.CancelFunc
	var busy bool
	sessionID := ""

	// Auto-submit the merge prompt as the first turn, then keep the socket open so
	// the user can steer (e.g. "the conflict in X should keep our version").
	runTurn := func(prompt string) {
		ctx, cancel := context.WithCancel(r.Context())
		turnMu.Lock()
		turnCancel = cancel
		busy = true
		turnMu.Unlock()
		newSession, canceled := d.runChatTurn(ctx, claudeBin, checkout, tools, prompt, sessionID, send)
		turnMu.Lock()
		sessionID = newSession
		busy = false
		if turnCancel != nil {
			turnCancel()
			turnCancel = nil
		}
		turnMu.Unlock()
		if canceled {
			_ = send(chatServerMsg{Type: "canceled"})
		}
		_ = send(chatServerMsg{Type: "turn_end"})
	}

	go runTurn(mergePrompt)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			turnMu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			turnMu.Unlock()
			return
		}
		var msg chatClientMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Action == "cancel" {
			turnMu.Lock()
			if turnCancel != nil {
				turnCancel()
			}
			turnMu.Unlock()
			continue
		}
		turnMu.Lock()
		running := busy
		turnMu.Unlock()
		if running || strings.TrimSpace(msg.Prompt) == "" {
			continue
		}
		go runTurn(msg.Prompt)
	}
}

// hostMergeCheckoutPath is the throwaway host checkout dir for a merge-with-host
// job, under the managed workspaces area keyed by PR id (so concurrent jobs on
// different PRs don't collide; the same PR reuses/clears its dir).
func hostMergeCheckoutPath(prID int64) string {
	return filepath.Join(config.CorralHome(), "merge-host", fmt.Sprintf("pr-%d", prID))
}
