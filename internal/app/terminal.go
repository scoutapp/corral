package app

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/jackrothrock/sandclaude/internal/session"
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
// Everything ships inside the sandclaude binary (Go code + embedded xterm.js
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

// handleTerminalPage serves the xterm.js host page for a project's Terminal tab.
// The page itself opens the WebSocket (see webui/static/terminal.js); this only
// needs to confirm the project exists and that a tmux dev session is present so
// we can render an actionable message instead of a blank terminal that never
// connects.
func (d *dashboardServer) handleTerminalPage(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sessionName := session.TmuxSessionNameForWorkspace(workspace)
	data := struct {
		ID        string
		Session   string
		SessionUp bool
	}{ID: id, Session: sessionName, SessionUp: session.TmuxSessionExists(sessionName)}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplates.ExecuteTemplate(w, "terminal.html.tmpl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleTerminalWS upgrades to a WebSocket and bridges it to a PTY running
// `tmux attach-session`. Attaching (rather than spawning a fresh shell) is what
// makes the browser terminal show the same live session as `sandclaude dev` and
// redraw at the current screen — tmux redraws and keeps scrollback on every
// attach, so no state needs tracking here beyond the process lifetime.
func (d *dashboardServer) handleTerminalWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	d.bridgeSessionWS(w, r, session.TmuxSessionNameForWorkspace(workspace),
		"no tmux dev session for this project — start it with `sandclaude dev`")
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

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}
	defer conn.Close()

	// -t forces a PTY; without it tmux refuses to attach ("open terminal failed:
	// not a terminal").
	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
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
