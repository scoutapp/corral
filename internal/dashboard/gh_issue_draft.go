package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/repos"
)

// ----------------------------------------------------------------------------
// AI-assisted GitHub issue drafting.
//
// WS /gh/issues/draft?repoId=<id>   client sends { "description": "..." }
//
// Runs the operator's HOST `claude` (NOT sandboxed — same trust basis + read-only
// tool set as the "Ask Claude" chat panel: Read/Grep/Glob only) inside a fresh
// TEMP checkout of the repo's cache mirror, so it can research the actual code
// before drafting. Two turns:
//   1. research (streamed to the browser like the chat panel), then
//   2. a resumed turn that outputs ONLY {"title","body"} JSON,
// which is parsed and sent as a final {type:"draft"} frame to fill the form.
//
// Drafting NEVER creates the issue — the user reviews the filled fields and
// clicks Create. The temp checkout is removed when the socket closes.
// ----------------------------------------------------------------------------

func (d *dashboardServer) handleGhIssueDraftWS(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repoId")
	if repoID == "" {
		http.Error(w, "repoId required", http.StatusBadRequest)
		return
	}
	if _, err := repos.Get(repoID); err != nil {
		http.NotFound(w, r)
		return
	}
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	rawSend := func(m chatServerMsg) error { return conn.WriteJSON(m) }
	// Capture the draft conversation (both turns in one row); best-effort.
	capt, send, finalize := d.captureSend(context.Background(), convOrigin{Kind: "issue-draft", RepoID: repoID}, rawSend)
	defer finalize("done")

	// First (only) client message carries the rough description.
	_, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var in struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(data, &in)
	if strings.TrimSpace(in.Description) == "" {
		_ = send(chatServerMsg{Type: "error", Text: "a description is required"})
		return
	}
	capt.recordPrompt(in.Description)

	// Fresh temp checkout of the repo mirror so claude can read the code. Removed
	// when we return (socket close / done).
	tmpRoot := filepath.Join(config.CorralHome(), "tmp")
	_ = os.MkdirAll(tmpRoot, 0755)
	tmp, err := os.MkdirTemp(tmpRoot, "issue-draft-*")
	if err != nil {
		// Fall back to the system temp dir if ~/.corral/tmp isn't writable.
		tmp, err = os.MkdirTemp("", "issue-draft-*")
	}
	if err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "could not create temp dir: " + err.Error()})
		return
	}
	defer os.RemoveAll(tmp)

	checkout := filepath.Join(tmp, "repo")
	_ = send(chatServerMsg{Type: "text", Text: "Checking out the repo to research…\n"})
	if err := repos.CloneLocal(repoID, checkout, ""); err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "checkout failed: " + err.Error()})
		return
	}

	// Cancel the claude turns if the browser disconnects mid-draft.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				cancel()
				return
			}
		}
	}()

	// Turn 1 — research, streamed. Read-only tools (Read/Grep/Glob), running in the
	// checkout so claude sees the real repo.
	researchPrompt := "You are drafting a GitHub issue for this repository. " +
		"Research the codebase (read relevant files, grep, explore the structure) so the issue is concrete and grounded in the actual code. " +
		"The user's request:\n\n" + in.Description + "\n\n" +
		"Investigate, then briefly summarize what you found and what the issue should cover. Do not write the final issue yet."
	sessionID, canceled := d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, researchPrompt, "", send)
	if canceled {
		return
	}

	// Turn 2 — format as JSON only (resumed, so it keeps the research context).
	_ = send(chatServerMsg{Type: "text", Text: "\nDrafting the issue…\n"})
	formatPrompt := "Now output the GitHub issue as a single JSON object and NOTHING else — no prose, no code fence. " +
		"Shape: {\"title\": \"<concise imperative title>\", \"body\": \"<markdown body with context, findings, and a clear ask>\"}."
	// Collect the final assistant text of turn 2 to parse the JSON out of.
	var buf strings.Builder
	collect := func(m chatServerMsg) error {
		if m.Type == "text" {
			buf.WriteString(m.Text)
		}
		// Don't forward turn-2's raw text to the UI; we only want the parsed draft.
		if m.Type == "error" {
			return send(m)
		}
		return nil
	}
	_, canceled = d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, formatPrompt, sessionID, collect)
	if canceled {
		return
	}

	title, body := parseIssueDraft(buf.String())
	if title == "" && body == "" {
		// Couldn't parse structured output — hand back the raw text as the body so
		// nothing is lost; the user can shape it.
		body = strings.TrimSpace(buf.String())
	}
	_ = send(chatServerMsg{Type: "draft", Text: title, Result: body})
}

// parseIssueDraft pulls {"title","body"} out of claude's turn-2 output. Tolerant
// of an accidental code fence or leading/trailing prose by scanning for the
// outermost JSON object.
func parseIssueDraft(s string) (title, body string) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return "", ""
	}
	var obj struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err != nil {
		return "", ""
	}
	return strings.TrimSpace(obj.Title), strings.TrimSpace(obj.Body)
}
