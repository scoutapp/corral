package dashboard

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/jackrothrock/sandclaude/internal/session"
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
