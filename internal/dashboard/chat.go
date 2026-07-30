package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ----------------------------------------------------------------------------
// "Ask Claude" chat panel — a Claude-Desktop-style chat UI backed by the host
// `claude` in headless stream-json mode (NOT a PTY / TUI). Each user turn spawns
// `claude -p <prompt> --output-format stream-json`; the JSON event stream is
// parsed and forwarded to the browser as clean typed frames that the frontend
// renders as message bubbles. Multi-turn continuity uses --resume with the
// session_id emitted in the first turn's init event.
//
// IMPORTANT: unlike the Container Shell (which runs inside the sandbox), this
// runs the OPERATOR'S OWN host `claude` — their real credentials/subscription,
// NOT the Anthropic API and NOT the sandboxed instance. It therefore executes
// with the operator's full host privileges (same trust basis as the host-shell
// overlay: acceptable only because the dashboard is loopback-only + token-gated).
// As defense-in-depth it starts with a read-only tool set (Read/Grep/Glob); the
// user grants more per session via the `tools` query param, which we validate
// against a fixed whitelist so the client can never widen it to an unknown tool.
// The panel surfaces a visible "not sandboxed" warning to the end user.
// ----------------------------------------------------------------------------

// chatDefaultTools is the capability a freshly-opened chat starts with: enough
// to answer questions about the repo, nothing that mutates the host.
var chatDefaultTools = []string{"Read", "Grep", "Glob"}

// chatToolWhitelist bounds what a client may request via ?tools=. Anything not
// listed here is dropped — a grant is opt-in and can never name an unknown tool.
var chatToolWhitelist = map[string]bool{
	"Read": true, "Grep": true, "Glob": true,
	"Edit": true, "Write": true, "Bash": true,
}

// parseChatTools turns the `tools` query param (comma-separated) into a
// validated allow-list. Empty/absent → the read-only default. Unknown names are
// silently ignored; if nothing valid remains, we fall back to the default.
func parseChatTools(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return chatDefaultTools
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if chatToolWhitelist[t] && !seen[t] {
			out = append(out, t)
			seen[t] = true
		}
	}
	if len(out) == 0 {
		return chatDefaultTools
	}
	return out
}

// chatClientMsg is what the browser sends per user turn.
type chatClientMsg struct {
	Prompt string `json:"prompt"`
}

// chatServerMsg is a typed frame the server forwards to the browser. Only the
// fields relevant to a given Type are populated.
type chatServerMsg struct {
	Type    string `json:"type"`              // "text" | "tool_use" | "result" | "error" | "turn_end"
	Text    string `json:"text,omitempty"`    // assistant text / error message
	Tool    string `json:"tool,omitempty"`    // tool name for tool_use
	CostUSD string `json:"costUsd,omitempty"` // formatted cost on result
	Model   string `json:"model,omitempty"`   // model id on result
	IsError bool   `json:"isError,omitempty"` // result subtype != success
}

// handleChatWS runs a Claude-Desktop-style chat over a WebSocket. Each client
// message is one user turn; the server spawns the host `claude` in headless
// stream-json mode, parses the event stream, and forwards typed frames the
// browser renders as bubbles. session_id from the first turn is reused via
// --resume so the conversation is multi-turn. Each WS connection is one
// independent conversation; multiple panels run concurrently.
func (d *dashboardServer) handleChatWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}
	tools := parseChatTools(r.URL.Query().Get("tools"))

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// writeMu serializes writes; the read loop and per-turn streaming both write.
	var writeMu sync.Mutex
	send := func(m chatServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(m)
	}

	sessionID := "" // captured from the first turn's init event, reused via --resume
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return // client closed
		}
		var msg chatClientMsg
		if json.Unmarshal(data, &msg) != nil || strings.TrimSpace(msg.Prompt) == "" {
			continue
		}
		sessionID = d.runChatTurn(r.Context(), claudeBin, workspace, tools, msg.Prompt, sessionID, send)
		_ = send(chatServerMsg{Type: "turn_end"})
	}
}

var (
	claudeBinMu     sync.Mutex
	claudeBinCached string
)

// resolveClaudeBin locates the host `claude` executable, entirely within the
// daemon so it works regardless of how or from where the dashboard was launched.
// The detached daemon runs with a stripped PATH that usually omits the
// version-manager dirs (nvm/asdf/…) where `claude` lives, and the launch-time
// PATH capture only helps when the launcher itself had claude on PATH — so we
// probe several strategies, in cheapest-first order, and cache the result:
//
//  1. SANDCLAUDE_CLAUDE_BIN — absolute path captured by the launcher (best case)
//  2. exec.LookPath — the daemon's own PATH
//  3. known install locations — nvm node bins, ~/.claude, ~/.local/bin,
//     homebrew, bun/deno, asdf shims
//  4. an interactive login shell — last resort, since nvm/asdf typically live in
//     interactive rc files that non-interactive shells don't source
func resolveClaudeBin() (string, error) {
	claudeBinMu.Lock()
	defer claudeBinMu.Unlock()
	// Reuse a previously-resolved path if it's still valid; re-probe otherwise so
	// a claude installed after the daemon started is eventually picked up, and a
	// one-time failure isn't cached forever.
	if claudeBinCached != "" && isExecutable(claudeBinCached) {
		return claudeBinCached, nil
	}
	p, err := findClaudeBin()
	if err == nil {
		claudeBinCached = p
	}
	return p, err
}

func findClaudeBin() (string, error) {
	// 1. Launch-time capture.
	if p := os.Getenv("SANDCLAUDE_CLAUDE_BIN"); p != "" {
		if isExecutable(p) {
			return p, nil
		}
	}
	// 2. Daemon PATH.
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	// 3. Known install locations. nvm may hold several node versions; take the
	// highest-sorting one (newest, lexically, for vNN.NN.NN names).
	home, _ := os.UserHomeDir()
	var candidates []string
	if home != "" {
		if matches, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin/claude")); len(matches) > 0 {
			sort.Strings(matches)
			candidates = append(candidates, matches[len(matches)-1])
		}
		candidates = append(candidates,
			filepath.Join(home, ".claude/local/claude"),
			filepath.Join(home, ".claude/bin/claude"),
			filepath.Join(home, ".local/bin/claude"),
			filepath.Join(home, ".bun/bin/claude"),
			filepath.Join(home, ".deno/bin/claude"),
			filepath.Join(home, ".asdf/shims/claude"),
		)
	}
	candidates = append(candidates,
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
	)
	for _, p := range candidates {
		if isExecutable(p) {
			return p, nil
		}
	}
	// 4. Interactive login shell (sources nvm/asdf rc), sanitized to the last line.
	if shell := os.Getenv("SHELL"); shell != "" {
		out, err := exec.Command(shell, "-ic", "command -v claude").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				line = strings.TrimSpace(line)
				if isExecutable(line) {
					return line, nil
				}
			}
		}
	}
	return "", fmt.Errorf("claude executable not found")
}

// isExecutable reports whether path is a regular, executable file.
func isExecutable(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0111 != 0
}

// runChatTurn spawns one `claude -p` invocation and streams its parsed events to
// the browser via send. Returns the (possibly updated) session id to thread into
// the next turn's --resume.
func (d *dashboardServer) runChatTurn(ctx context.Context, claudeBin, workspace string, tools []string, prompt, sessionID string, send func(chatServerMsg) error) string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "default",
		"--allowedTools",
	}
	args = append(args, tools...)
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	if _, serr := os.Stat(workspace); serr == nil {
		cmd.Dir = workspace // land in the project so `claude` sees the repo
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "failed to start claude: " + err.Error()})
		return sessionID
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "failed to start claude: " + err.Error()})
		return sessionID
	}

	// Parse the newline-delimited JSON event stream. Buffer is generous because a
	// single assistant event can be large.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		sessionID = parseChatEvent(sc.Bytes(), sessionID, send)
	}
	_ = cmd.Wait()
	return sessionID
}

// parseChatEvent decodes one stream-json line and forwards the browser-relevant
// bits. Unknown/uninteresting event types (rate_limit_event, etc.) are ignored.
// Returns the session id, updated if this event carried one.
func parseChatEvent(line []byte, sessionID string, send func(chatServerMsg) error) string {
	var ev struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Message   struct {
			Model   string `json:"model"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
		Result    string  `json:"result"`
		IsError   bool    `json:"is_error"`
		TotalCost float64 `json:"total_cost_usd"`
		Model     string  `json:"model"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return sessionID
	}
	if ev.SessionID != "" {
		sessionID = ev.SessionID
	}
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text != "" {
					_ = send(chatServerMsg{Type: "text", Text: c.Text})
				}
			case "tool_use":
				_ = send(chatServerMsg{Type: "tool_use", Tool: c.Name})
			}
		}
	case "result":
		cost := ""
		if ev.TotalCost > 0 {
			cost = formatUSD(ev.TotalCost)
		}
		_ = send(chatServerMsg{
			Type: "result", IsError: ev.Subtype != "success",
			CostUSD: cost, Model: ev.Model,
		})
	}
	return sessionID
}

// formatUSD renders a cost like "$0.21".
func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

// handleChatPage serves the chat panel page for the iframe. The page's chat.js
// opens a WebSocket to /chat/ws and renders bubbles from the typed frames.
func (d *dashboardServer) handleChatPage(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := struct {
		ID        string
		Workspace string
	}{ID: id, Workspace: workspace}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplates.ExecuteTemplate(w, "chat.html.tmpl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
