package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackrothrock/sandclaude/internal/session"
	sshagent "github.com/jackrothrock/sandclaude/internal/ssh"
)

// handleStartProject cold-starts a project's container from the dashboard.
//
//	POST /p/<id>/start
//
// Today the dashboard only ever RESTARTED a project (handleConfigRestart);
// starting a freshly-created one is new. We shell out to this same sandclaude
// binary (`sandclaude dev`, detached, in the workspace) so there is ONE start
// path shared with the CLI, rather than re-implementing container orchestration.
//
// The child is launched THROUGH the operator's login shell so it inherits the
// real interactive PATH (docker/git/claude/tmux live in version-manager dirs the
// detached daemon's stripped PATH usually omits — the same problem the chat panel
// hit). Same host-side trust basis as the host shell.
func (d *dashboardServer) handleStartProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Already running? Report success idempotently.
	if session.DockerContainerRunning(session.ContainerNameForWorkspace(workspace)) {
		writeFilesJSON(w, map[string]any{"ok": true, "already": true})
		return
	}

	// tmux is a hard host dependency (the interactive container runs inside a host
	// tmux session). Fail with a clear message instead of a cryptic downstream one.
	if err := session.RequireTmux(); err != nil {
		w.WriteHeader(http.StatusPreconditionFailed)
		writeFilesJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}

	// Pre-load gate: the child `sandclaude dev` runs detached (no TTY), so if this
	// project has ssh keys configured but they aren't loaded into the scoped agent
	// yet, the child would fail fast. Surface that here so the caller can send the
	// user to the Config tab's "Load keys" flow first (design: pre-load, then start).
	if keys := resolveProjectSSHKeys(workspace); len(keys) > 0 {
		// Coverage check: confirm EVERY resolved key is loaded, not just that the
		// agent holds ≥1 identity — otherwise a project key added on top of the
		// (silently-keychain-loaded) global key never gets prompted for.
		ag, aerr := sshagent.Ensure(ProjectID(workspace), keys)
		if aerr != nil || ag == nil {
			http.Error(w, "ssh agent unavailable", http.StatusInternalServerError)
			return
		}
		// Before demanding an interactive passphrase, try the macOS Keychain: if
		// the passphrase was stored on a prior load, this loads the keys silently
		// (no re-typing) — the whole point of "ask once, reuse". No-op on Linux.
		if !ag.AllKeysLoaded() {
			ag.TryLoadFromKeychain()
		}
		if !ag.AllKeysLoaded() {
			w.WriteHeader(http.StatusConflict)
			writeFilesJSON(w, map[string]any{
				"ok":               false,
				"ssh_keys_pending": true,
				"message":          "ssh keys need loading first — use Config → SSH keys → Load keys, then start",
			})
			return
		}
	}

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// `<shell> -lc 'exec "$0" dev' <sandclaude-abs-path>`: login shell for the full
	// PATH, exec our own binary by absolute path (no lookup needed), args passed
	// positionally so nothing is interpolated into the shell string.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell, "-lc", `exec "$0" dev`, exe)
	cmd.Dir = workspace
	if err := cmd.Start(); err != nil {
		http.Error(w, "start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Detach: we don't wait. The dashboard's status poll will show it come up.
	go func() { _ = cmd.Wait() }()

	writeFilesJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("starting %s", session.ContainerNameForWorkspace(workspace))})
}

// handleStopProject stops a running project's container from the dashboard.
//
//	POST /p/<id>/stop
//
// Tears down the container and its detached tmux session (the inverse of start).
// `docker rm -f` (not `kill`) stops AND removes synchronously so a subsequent
// start doesn't race the async --rm cleanup on the container name; kill-session
// clears the tmux session so a later start's `new-session` doesn't collide with a
// dead pane. Idempotent — a project that's already down reports success. The
// scoped ssh-agent is left as-is (a later start re-adopts or re-prompts).
func (d *dashboardServer) handleStopProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	container := session.ContainerNameForWorkspace(workspace)
	tmuxSession := session.TmuxSessionNameForWorkspace(workspace)

	if !session.DockerContainerRunning(container) {
		// Still clear any lingering dead-pane session so the pane goes fully idle.
		_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()
		writeFilesJSON(w, map[string]any{"ok": true, "already": true})
		return
	}

	_ = exec.Command("docker", "rm", "-f", container).Run()
	_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()

	writeFilesJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("stopping %s", container)})
}

// claudeReady reports whether a captured tmux pane shows Claude's input prompt
// ready to accept text. Claude's TUI draws a `❯` prompt line and a "bypass
// permissions" footer once it's up; either is a reliable "ready" signal and both
// only appear after boot completes (so we won't type into the boot log).
func claudeReady(pane string) bool {
	return strings.Contains(pane, "❯") ||
		strings.Contains(pane, "bypass permissions") ||
		strings.Contains(pane, "shift+tab to cycle")
}

// handlePopulatePrompt types a prompt INTO the project's Claude input without
// submitting it (tmux send-keys, no Enter), once the container's dev session is
// up. Used after spawning a project off a GitHub issue: the user reviews the
// pre-typed prompt and presses Enter themselves.
//
//	POST /p/<id>/populate-prompt   { "prompt": "..." }
//
// The session may not exist yet (the container is still booting), so we poll in
// the background and return immediately; the prompt lands whenever Claude's
// session appears, or gives up after a bounded wait.
func (d *dashboardServer) handlePopulatePrompt(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	sess := session.TmuxSessionNameForWorkspace(workspace)
	prompt := body.Prompt
	go func() {
		// The session existing is NOT enough: it's created by `docker run` in tmux,
		// but the container then boots (proxy, dockerd, launcher) for a while before
		// Claude's TUI actually appears and can accept input. Typing during that
		// window is lost. So poll the PANE CONTENT until Claude's input prompt is
		// drawn (its `❯` prompt / "bypass permissions" footer), then type.
		//
		// Up to ~5 min (cold boot + image build can be slow). Once ready, a short
		// settle, then send-keys WITHOUT Enter so the prompt sits unsubmitted.
		for i := 0; i < 300; i++ {
			time.Sleep(1 * time.Second)
			if !session.TmuxSessionExists(sess) {
				continue
			}
			out, _ := exec.Command("tmux", "capture-pane", "-t", sess, "-p").Output()
			if claudeReady(string(out)) {
				time.Sleep(1500 * time.Millisecond) // let the input line settle
				_ = exec.Command("tmux", "send-keys", "-t", sess, "--", prompt).Run()
				return
			}
		}
	}()

	writeFilesJSON(w, map[string]any{"ok": true, "message": "prompt will be typed into Claude once its input is ready"})
}
