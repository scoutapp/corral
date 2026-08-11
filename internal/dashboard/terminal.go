package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/scoutapp/corral/internal/session"
)

// ----------------------------------------------------------------------------
// Browser terminal — a self-contained PTY-over-WebSocket bridge that replaces
// the previous external `ttyd` dependency.
//
// Flow: the Terminal tab's iframe loads an xterm.js page (handleTerminalPage),
// which opens a WebSocket to handleTerminalWS. That handler starts
// `tmux attach-session -t <session>` on a PTY and pumps bytes both ways —
// PTY output → ws (as binary frames), ws input → PTY. A small JSON control
// frame ({"type":"resize",...}) carries terminal resize events through to
// TIOCSWINSZ via pty.Setsize.
//
// Everything ships inside the corral binary (Go code + embedded xterm.js
// assets), so there is no external terminal program to install — the reason
// this exists instead of ttyd.
// ----------------------------------------------------------------------------

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin only: the dashboard is loopback-bound and token-gated, and the
	// terminal grants a real shell — don't accept cross-origin WebSocket opens.
	// An empty/absent Origin (non-browser client) is rejected too.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		return origin == "http://"+r.Host
	},
}

// termSession tracks one live browser terminal so shutdown() can tear it down.
type termSession struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

// terminalResize is the only structured client→server control message. Regular
// keystrokes arrive as raw binary frames; resize needs the extra dimensions so
// it's sent as a text/JSON frame and demultiplexed by opcode in handleTerminalWS.
type terminalResize struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleTerminalWS upgrades to a WebSocket and bridges it to a PTY running
// `tmux attach-session`. Attaching (rather than spawning a fresh shell) is what
// makes the browser terminal show the same live session as `corral dev` and
// redraw at the current screen — tmux redraws and keeps scrollback on every
// attach, so no state needs tracking here beyond the process lifetime.
func (d *dashboardServer) handleTerminalWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sessionName := session.TmuxSessionNameForWorkspace(workspace)
	// Don't attach to a dead-pane session — after the container's `docker run`
	// exits, `remain-on-exit on` keeps the session alive with a dead pane, and
	// attaching to it just shows tmux's blank "Pane is dead" fill. Treat that as
	// not-running so the dashboard shows the ▶ Start empty state instead.
	if !session.TmuxSessionLive(sessionName) {
		http.Error(w, "this project isn't running — press ▶ Start in the dashboard", http.StatusBadGateway)
		return
	}
	d.bridgeSessionWS(w, r, sessionName,
		"this project isn't running — press ▶ Start in the dashboard")
}

// handleContainerWS bridges a browser terminal to an interactive shell INSIDE
// the project's Docker container via `docker exec -it <container> bash`. Unlike
// the tmux terminal (which mirrors the Claude dev session), this is a fresh shell
// for poking around the container's filesystem. Gated on the container running.
func (d *dashboardServer) handleContainerWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	containerName := session.ContainerNameForWorkspace(workspace)
	if !session.DockerContainerRunning(containerName) {
		http.Error(w, "container is not running for this project", http.StatusBadGateway)
		return
	}
	// -it: interactive + TTY so the shell behaves like a real terminal. We launch
	// bash INTERACTIVE (-i) because the sandbox user's ~/.bashrc sets the colored
	// user@host:cwd$ PS1 but guards it with `case $- in *i*)`, so a bare
	// `exec bash` starts non-interactive, .bashrc returns early, and PS1 is empty
	// (the "I don't realize I'm in a shell" symptom).
	//
	// Do NOT redirect bash's stderr: an interactive bash writes its PROMPT (and
	// readline) to stderr, so `2>/dev/null` both hides the prompt and breaks the
	// interactive session. Pick bash-vs-sh with `command -v` instead of a stderr
	// hack, and exec the interactive shell with its stderr intact.
	cmd := exec.Command("docker", "exec", "-it", containerName,
		"sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -i; else exec sh -i; fi")
	d.bridgePTY(w, r, cmd)
}

// hostShellSession returns the per-project tmux session name backing that
// project's host shell. It's distinct from the Claude dev session
// (TmuxSessionNameForWorkspace) — a "-host" suffix — so the integrated host
// terminal is its own long-lived session that survives navigation and reloads.
func hostShellSession(workspace string) string {
	return session.TmuxSessionNameForWorkspace(workspace) + "-host"
}

// handleTerminalAction runs a tmux operation (split/close/clear) against a
// project's tmux-backed terminal, driving the right-click menu in the dashboard.
// Body: {"kind":"claude"|"host","action":"split-h"|"split-v"|"kill-pane"|"clear"}.
// The container shell is a bare `docker exec` (no tmux), so it isn't handled here
// — its menu offers only client-side copy/paste/clear.
func (d *dashboardServer) handleTerminalAction(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Map the terminal kind to its tmux session.
	var sess string
	switch body.Kind {
	case "claude":
		sess = session.TmuxSessionNameForWorkspace(workspace)
	case "host":
		sess = hostShellSession(workspace)
	default:
		http.Error(w, "split/close not available for this terminal", http.StatusBadRequest)
		return
	}
	if !session.TmuxSessionExists(sess) {
		http.Error(w, "terminal not running", http.StatusBadGateway)
		return
	}

	// Translate the action to a tmux command targeting the session's active pane.
	// -h = split into left|right, -v = split into top/bottom (tmux's axis naming
	// is the opposite of the visual, hence the labels the UI uses).
	var args []string
	switch body.Action {
	case "split-h":
		args = []string{"split-window", "-h", "-t", sess}
	case "split-v":
		args = []string{"split-window", "-v", "-t", sess}
	case "kill-pane":
		args = []string{"kill-pane", "-t", sess}
	case "clear":
		args = []string{"send-keys", "-t", sess, "clear", "Enter"}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	// Start new panes in the workspace dir for splits.
	if (body.Action == "split-h" || body.Action == "split-v") && workspace != "" {
		if _, serr := os.Stat(workspace); serr == nil {
			args = append(args, "-c", workspace)
		}
	}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		http.Error(w, "tmux "+body.Action+" failed: "+strings.TrimSpace(string(out)), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleHostWS bridges a browser terminal to a real shell on the HOST (the
// machine running the dashboard), started in the project's workspace directory.
// This is the VS Code-style "integrated terminal" — unlike the container shell,
// it is NOT sandboxed; it runs with the operator's full privileges. That's
// acceptable because the dashboard is loopback-only and token-gated (the same
// gate that guards the container shell), and the operator is the machine owner.
//
// The shell runs inside a PERSISTENT per-project tmux session (created on first
// open, attached on every open thereafter). So leaving the project and coming
// back — or even reloading the browser — reattaches the SAME live shell with its
// cwd, running commands, and scrollback intact, rather than spawning a fresh one.
func (d *dashboardServer) handleHostWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sessionName := hostShellSession(workspace)

	// Create the session detached if it doesn't exist yet, rooted in the project
	// dir so the shell lands there. `tmux new-session -d` is a no-op error if it
	// already exists; we gate on has-session to avoid clobbering a live one.
	if !session.TmuxSessionExists(sessionName) {
		dir := workspace
		if _, serr := os.Stat(workspace); serr != nil {
			dir = "" // workspace gone; let tmux use the default cwd
		}
		args := []string{"new-session", "-d", "-s", sessionName}
		if dir != "" {
			args = append(args, "-c", dir)
		}
		// Make the session feel like a plain terminal, not a tmux UI:
		//   status off  — no tmux status bar
		//   mouse on     — scroll wheel scrolls scrollback, click focuses a pane
		//                  (xterm still gives native shift+drag select/copy)
		if err := exec.Command("tmux", args...).Run(); err == nil {
			exec.Command("tmux", "set-option", "-t", sessionName, "status", "off").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "mouse", "on").Run()
		}
	}

	// Attach (forces a PTY via -t inside bridgeSessionWS). Detaching on tab close
	// leaves the session alive for next time.
	d.bridgeSessionWS(w, r, sessionName, "could not start host shell")
}

// handleUpdateWS bridges a browser terminal to `corral update` running on
// the HOST. This is the dashboard's "Update" button: rather than a silent
// privileged endpoint, it opens a real PTY so the update runs with the operator's
// TTY — sudo (if the binary lives somewhere unwritable) can prompt, the
// confirmation prompt works, and the operator sees the full output. We run the
// SAME binary serving this dashboard (os.Executable) so the update targets the
// actual install, and pass --yes since the click IS the consent. After it exits
// we drop into an interactive shell so the log stays on screen and the tab isn't
// dead.
func (d *dashboardServer) handleUpdateWS(w http.ResponseWriter, r *http.Request) {
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, "cannot locate the corral binary", http.StatusInternalServerError)
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	// Run the update, then hand off to an interactive shell so output persists and
	// the user can act on any printed instruction (e.g. a sudo command). Quote the
	// exe path for safety against spaces.
	script := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "' update --yes; " +
		"echo; echo '[update finished — this shell stays open; close the tab when done]'; exec " + shell + " -l"
	cmd := exec.Command(shell, "-lc", script)
	d.bridgePTY(w, r, cmd)
}

// handleSessionWS bridges a browser terminal to a named tmux session directly
// (not a project's dev session) — used for the interactive `populate-proxy-
// credentials` flow the dashboard spawns into its own tmux session.
func (d *dashboardServer) handleSessionWS(w http.ResponseWriter, r *http.Request, session string) {
	d.bridgeSessionWS(w, r, session, "session not running")
}

// bridgeSessionWS upgrades to a WebSocket and bridges it to a PTY running
// `tmux attach-session -t <session>`. Attaching (rather than spawning a shell)
// makes the browser terminal mirror the live session and redraw at the current
// screen; closing the tab detaches without killing the session.
func (d *dashboardServer) bridgeSessionWS(w http.ResponseWriter, r *http.Request, sessionName, missingMsg string) {
	if !session.TmuxSessionExists(sessionName) {
		http.Error(w, missingMsg, http.StatusBadGateway)
		return
	}
	// -t forces a PTY; without it tmux refuses to attach ("open terminal failed:
	// not a terminal").
	d.bridgePTY(w, r, exec.Command("tmux", "attach-session", "-t", sessionName))
}

// bridgePTY upgrades to a WebSocket and bridges it to a PTY running the given
// command: PTY output -> binary ws frames, binary ws frames -> PTY input, and a
// JSON {"type":"resize"} control frame -> TIOCSWINSZ. Shared by the tmux-attach
// terminal, the populate-credentials session, and the container shell (which
// runs `docker exec -it`). The caller is responsible for any liveness precheck.
func (d *dashboardServer) bridgePTY(w http.ResponseWriter, r *http.Request, cmd *exec.Cmd) {
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}
	defer conn.Close()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("\r\n[failed to start terminal: "+err.Error()+"]\r\n"))
		return
	}

	sess := &termSession{ptmx: ptmx, cmd: cmd}
	d.mu.Lock()
	d.terms[sess] = struct{}{}
	d.mu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			ptmx.Close()
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			cmd.Wait()
			d.mu.Lock()
			delete(d.terms, sess)
			d.mu.Unlock()
		})
	}
	defer cleanup()

	go func() {
		defer cleanup()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
					time.Now().Add(time.Second))
				return
			}
		}
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			if _, err := ptmx.Write(data); err != nil {
				return
			}
		case websocket.TextMessage:
			var msg terminalResize
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
			}
		}
	}
}

// shutdownTerminals closes every live browser-terminal PTY. Like the old ttyd
// shutdown it never touches the underlying tmux sessions — those outlive the
// dashboard by design (killing the `attach` client only detaches).
func (d *dashboardServer) shutdownTerminals() {
	d.mu.Lock()
	sessions := make([]*termSession, 0, len(d.terms))
	for s := range d.terms {
		sessions = append(sessions, s)
	}
	d.terms = make(map[*termSession]struct{})
	d.mu.Unlock()

	for _, s := range sessions {
		s.ptmx.Close()
		if s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
	}
}
