package dashboard

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// ----------------------------------------------------------------------------
// "Ask Claude" chat panel — an interactive `claude` session bridged to the
// browser over the same PTY-over-WebSocket mechanism as the terminals.
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

// handleChatWS bridges a browser terminal to an interactive host `claude`,
// started in the project's workspace directory so it can read the repo with its
// own tools. Each connection spawns its own process, so multiple chat panels run
// concurrently and independently. No session persistence — the process lives for
// as long as the panel is open.
func (d *dashboardServer) handleChatWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		http.Error(w, "the `claude` CLI was not found on the host PATH — install Claude Code to use the chat panel", http.StatusBadGateway)
		return
	}

	tools := parseChatTools(r.URL.Query().Get("tools"))

	// --allowedTools <list>: start with only the granted tools. --permission-mode
	// default keeps Claude's normal prompt behaviour for anything not pre-allowed,
	// so a granted-but-not-listed action still requires interactive approval rather
	// than running silently.
	args := []string{"--permission-mode", "default", "--allowedTools"}
	args = append(args, tools...)

	cmd := exec.Command(claudeBin, args...)
	if _, serr := os.Stat(workspace); serr == nil {
		cmd.Dir = workspace // land in the project so `claude` sees the repo
	}
	d.bridgePTY(w, r, cmd)
}

// handleChatPage serves the xterm page for the chat panel iframe. Mirrors
// handleHostPage; the client (terminal.js) connects to /chat/ws via the
// data-ws-path attribute in chat.html.tmpl.
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
