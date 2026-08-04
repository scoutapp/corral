package dashboard

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

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

	// Pre-load gate: the child `sandclaude dev` runs detached (no TTY), so if this
	// project has ssh keys configured but they aren't loaded into the scoped agent
	// yet, the child would fail fast. Surface that here so the caller can send the
	// user to the Config tab's "Load keys" flow first (design: pre-load, then start).
	if keys := resolveProjectSSHKeys(workspace); len(keys) > 0 {
		if _, loaded := sshagent.Probe(ProjectID(workspace)); !loaded {
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
