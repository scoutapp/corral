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

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/config"
)

// withContextHint prepends a page-context note to a prompt on the first turn of a
// global chat, so the assistant knows where the user is ("this repo" resolves).
// firstTurn gates it — later turns already carry the context via --resume.
func withContextHint(prompt, hint string, firstTurn bool) string {
	if hint == "" || !firstTurn {
		return prompt
	}
	return "[Context: " + hint + "]\n\n" + prompt
}

// truncate shortens s to at most n runes, appending an ellipsis when clipped.
// Used for log messages built from free-text (chat prompts) so a row stays a
// readable one-liner.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

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

// chatClientMsg is what the browser sends. A "prompt" starts a turn; an
// action:"cancel" kills the currently-running turn.
type chatClientMsg struct {
	Prompt string `json:"prompt"`
	Action string `json:"action"` // "" (send prompt) | "cancel"
	// Ctx is a per-message page-context hint ("viewing repo X"). Sent with each
	// prompt so the WebSocket URL can stay STABLE across navigation — the context
	// travels with the turn instead of being baked into the connection URL (which
	// would force a reconnect, and a fresh Claude session, on every page change).
	// Only applied on the first turn (later turns already have it via --resume).
	Ctx string `json:"ctx,omitempty"`
}

// chatServerMsg is a typed frame the server forwards to the browser. Only the
// fields relevant to a given Type are populated.
type chatServerMsg struct {
	Type      string `json:"type"`                // "text" | "tool_use" | "tool_result" | "result" | "error" | "turn_end" | "canceled" | "session" | "conv_meta"
	Text      string `json:"text,omitempty"`      // assistant text / error message
	Tool      string `json:"tool,omitempty"`      // tool name for tool_use
	Input     string `json:"input,omitempty"`     // tool input (JSON) for tool_use
	Result    string `json:"result,omitempty"`    // tool result content for tool_result
	CostUSD   string `json:"costUsd,omitempty"`   // formatted cost on result
	Model     string `json:"model,omitempty"`     // model id on result
	IsError   bool   `json:"isError,omitempty"`   // result subtype != success
	SessionID string `json:"sessionId,omitempty"` // Claude session id (type "session"), for reload-resume
	ConvID    int64  `json:"convId,omitempty"`    // captured conversation id (type "conv_meta")
	ConvUUID  string `json:"convUuid,omitempty"`  // captured conversation UUID (type "conv_meta"), shown in host chat header
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
	origin := convOrigin{Kind: "project-chat", ProjectID: ProjectID(workspace), ProjectLabel: filepath.Base(workspace)}
	d.runChatSession(w, r, workspace, claudeBin, tools, "", r.URL.Query().Get("resume"), origin)
}

// handleGlobalChatWS is the app-wide "Claude everywhere" chat (/chat/ws) — not
// tied to any project. It runs the host claude in a neutral, contained working
// directory (~/.corral) rather than a repo, and its tool set comes from the
// configured global-chat capability (read-only vs act), NOT the ?tools= param —
// the global dock's power is a single deliberate setting, gated further by
// API-writes. Same host-Claude trust basis as the project chat.
func (d *dashboardServer) handleGlobalChatWS(w http.ResponseWriter, r *http.Request) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}
	cap, ok := d.ChatCapability()
	tools := globalChatTools(cap, ok)
	// A short context hint from the page the user is on ("viewing repo X"), so the
	// global assistant can resolve "this repo"/"this PR". Now sent per-message, but
	// still read here for backward compatibility. resume=<id> continues a prior
	// Claude session after a reload (the browser persists the id we emit).
	contextHint := r.URL.Query().Get("ctx")
	resume := r.URL.Query().Get("resume")
	// Empty workspace → runChatTurn runs in the global chat dir (~/.corral).
	d.runChatSession(w, r, "", claudeBin, tools, contextHint, resume, convOrigin{Kind: "global-chat"})
}

// runChatSession upgrades to a WebSocket and drives the turn loop for a chat,
// project-scoped (workspace set) or global (workspace ""). Shared by
// handleChatWS and handleGlobalChatWS.
func (d *dashboardServer) runChatSession(w http.ResponseWriter, r *http.Request, workspace, claudeBin string, tools []string, contextHint, resume string, origin convOrigin) {
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// writeMu serializes writes; the read loop and the running turn both write.
	var writeMu sync.Mutex
	rawSend := func(m chatServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(m)
	}

	// Tee every frame into the conversations DB (best-effort; never blocks the
	// live stream). One capturer per connection = one conversation across all its
	// turns. finalize stamps the terminal status when the socket closes.
	cap, send, finalize := d.captureSend(r.Context(), origin, rawSend)
	defer finalize("done")

	// turnCancel, when non-nil, cancels the in-flight turn's process; guarded by
	// turnMu since it's set by the read loop and cleared by the turn goroutine.
	var turnMu sync.Mutex
	var turnCancel context.CancelFunc
	var busy bool
	// Seeded from a client-supplied resume id (reload continuity); otherwise
	// captured from the first turn and reused via --resume within this connection.
	sessionID := resume

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// Client closed / socket error — cancel any running turn and exit.
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
			continue // ignore prompts while a turn is in flight, and empty prompts
		}

		// On the FIRST turn only, prepend the page-context hint (if any) so the
		// assistant knows where the user is. Later turns already have the context
		// via --resume, so we don't repeat it. The hint comes with THIS message
		// (msg.Ctx) — reflecting the page the user is on right now — falling back to
		// any connection-level hint (project chats still pass one at connect).
		hint := msg.Ctx
		if hint == "" {
			hint = contextHint
		}
		prompt := withContextHint(msg.Prompt, hint, sessionID == "")

		// Capture the user's prompt (the raw text, not the context-hinted wrapper)
		// before the turn — the stream doesn't echo it back. Best-effort. This also
		// ensures the conversation row exists so its id can drive cross-origin links.
		cap.recordPrompt(msg.Prompt)

		// Run the turn in a goroutine so the read loop stays responsive to cancel.
		// Carry the driving conversation id so runChatTurn can stamp it into the
		// spawned claude's env (cross-origin linkage).
		ctx, cancel := context.WithCancel(withConvID(r.Context(), cap.ConvID()))
		turnMu.Lock()
		turnCancel = cancel
		busy = true
		turnMu.Unlock()

		go func(prompt string) {
			// Each chat turn is the root of its own trace: whatever the host Claude
			// does in-turn (and, once #3/#4 land, the API actions it drives) nests
			// under this span. Untraced context → StartSpan mints a fresh trace_id.
			turnCtx, endTurn := d.applog().StartSpan(ctx, applog.Entry{
				Category: applog.CatChat, Event: "chat.turn",
				Message:   applog.Fmt("Chat turn: %s", truncate(prompt, 80)),
				ProjectID: ProjectID(workspace),
			})
			newSession, canceled := d.runChatTurn(turnCtx, claudeBin, workspace, tools, prompt, sessionID, send)
			endTurn(nil)
			turnMu.Lock()
			sessionID = newSession
			busy = false
			if turnCancel != nil {
				turnCancel() // release the context
				turnCancel = nil
			}
			turnMu.Unlock()
			if canceled {
				_ = send(chatServerMsg{Type: "canceled"})
			}
			_ = send(chatServerMsg{Type: "turn_end"})
		}(prompt)
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
//  1. CORRAL_CLAUDE_BIN — absolute path captured by the launcher (best case)
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
	if p := os.Getenv("CORRAL_CLAUDE_BIN"); p != "" {
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
// the next turn's --resume, and whether the turn was canceled (ctx done — the
// caller kills the process via the context).
// buildClaudeArgs assembles the `claude -p` argv for one chat turn. It omits
// --allowedTools entirely when no tools are granted: a bare "--allowedTools"
// with no following value is a malformed flag that makes `claude` fail — the
// bug that broke the PR-review chat (which grants no tools).
func buildClaudeArgs(prompt string, tools []string, sessionID string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "default",
	}
	// Load the corral-api skill for THIS chat session only, via --plugin-dir. We
	// deliberately don't install it into ~/.claude/skills: that would make its
	// description standing context in every host chat forever, even for users who
	// never drive the API. Scoping it to the Corral chat process keeps that cost
	// exactly where it's useful. Skipped silently if the bundled skill is absent.
	if dir := corralAPISkillDir(); dir != "" {
		args = append(args, "--plugin-dir", dir)
	}
	if len(tools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, tools...)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	return args
}

// corralAPISkillDir returns the bundled corral-api skill/plugin directory, or ""
// if it isn't present (dev checkout without the host tier, or a partial install).
// A plugin dir is identified by its .claude-plugin/plugin.json manifest.
func corralAPISkillDir() string {
	dir := filepath.Join(config.HostAssetsDir(), "skills", "corral-api")
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
		return ""
	}
	return dir
}

func (d *dashboardServer) runChatTurn(ctx context.Context, claudeBin, workspace string, tools []string, prompt, sessionID string, send func(chatServerMsg) error) (string, bool) {
	args := buildClaudeArgs(prompt, tools, sessionID)

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	// Cross-origin linkage: stamp the conversation driving THIS turn into the
	// subprocess env. If the spawned claude drives `corral api` (via the skill),
	// the CLI forwards this as a header so any work it kicks off (a worker, a
	// created project) records parent_conversation_id = this conversation — letting
	// the UI follow the causal chain across the request/process boundary.
	if convID := convIDFrom(ctx); convID > 0 {
		cmd.Env = append(os.Environ(), fmt.Sprintf("CORRAL_PARENT_CONVERSATION_ID=%d", convID))
		if tid := applog.TraceID(ctx); tid != "" {
			cmd.Env = append(cmd.Env, "CORRAL_PARENT_TRACE_ID="+tid)
		}
	}
	if workspace != "" {
		if _, serr := os.Stat(workspace); serr == nil {
			cmd.Dir = workspace // project chat: land in the repo so `claude` sees it
		}
	} else {
		// Global chat: run in a neutral, contained dir (~/.corral) — nothing
		// sensitive, stable, corral-owned. The global dock's real work is driving
		// the API over the network, not reading files, so the cwd is just
		// containment for Read/Grep.
		cmd.Dir = globalChatDir()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "failed to start claude: " + err.Error()})
		return sessionID, false
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		_ = send(chatServerMsg{Type: "error", Text: "failed to start claude: " + err.Error()})
		return sessionID, false
	}

	// Parse the newline-delimited JSON event stream. Buffer is generous because a
	// single assistant event can be large.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		sessionID = parseChatEvent(sc.Bytes(), sessionID, send)
	}
	_ = cmd.Wait()
	// ctx.Err() != nil means the caller canceled (Stop / socket close) rather than
	// the turn completing on its own.
	return sessionID, ctx.Err() != nil
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
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`   // tool_use arguments
				Content json.RawMessage `json:"content"` // tool_result payload (string or array)
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
	if ev.SessionID != "" && ev.SessionID != sessionID {
		sessionID = ev.SessionID
		// Tell the browser the session id so it can persist it and, after a reload,
		// reconnect with resume=<id> to continue the same Claude conversation.
		_ = send(chatServerMsg{Type: "session", SessionID: sessionID})
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
				_ = send(chatServerMsg{Type: "tool_use", Tool: c.Name, Input: string(c.Input)})
			}
		}
	case "user":
		// tool_result blocks arrive as user-role events. Surface the result text
		// so the panel can show what a tool returned.
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				_ = send(chatServerMsg{Type: "tool_result", Result: toolResultText(c.Content)})
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

// toolResultText normalizes a tool_result's content, which stream-json emits
// either as a bare string or as an array of {type:"text",text:...} blocks.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw) // fall back to the raw JSON
}

// formatUSD renders a cost like "$0.21".
func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}
