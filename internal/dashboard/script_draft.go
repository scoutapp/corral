package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ----------------------------------------------------------------------------
// AI-assisted bash-script drafting for automation steps.
//
// WS /api/scripts/draft   client sends { "description": "..." }
//
// Runs the operator's HOST `claude` (read-only tools) — no repo checkout needed;
// the script runs later in the sandbox with the CORRAL_* event vars. Two turns:
// research/plan (streamed), then emit ONLY the finished bash script. The result
// fills the step's script editor. Nothing is saved or executed here.
// ----------------------------------------------------------------------------

func (d *dashboardServer) handleScriptDraftWS(w http.ResponseWriter, r *http.Request) {
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
	// Capture the draft conversation (both turns land in one row); best-effort.
	capt, send, finalize := d.captureSend(context.Background(), convOrigin{Kind: "script-draft"}, rawSend)
	defer finalize("done")

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

	// The env the script will run with, so the model uses the right variables.
	const envNote = "The script runs in the Corral sandbox when an automation fires. These environment " +
		"variables are available: CORRAL_EVENT, CORRAL_REPO_ID, CORRAL_PR_NUMBER, CORRAL_PR_URL, " +
		"CORRAL_PR_TITLE, CORRAL_OWNER_NAME, CORRAL_HEAD_SHA (present depending on the event). " +
		"`gh` and `git` are on PATH (gh is authenticated via the proxy). Outbound hosts must be on the " +
		"firewall allowlist. " +
		"For SECRETS (API keys, tokens): read them from a plain UPPER_CASE env var (e.g. " +
		"$MYSERVICE_API_KEY) that the script does NOT assign itself — Corral detects such vars, lets the " +
		"user store the value in their Keychain, and injects it into the script's environment at run time. " +
		"Do not hardcode secrets or read them from a plaintext file. " +
		"Keep it POSIX-bash, robust, and quiet on success."

	plan := "You are writing a bash script for a Corral automation step. " + envNote + "\n\n" +
		"The user wants a script that: " + in.Description + "\n\n" +
		"Briefly outline the approach (commands, checks). Do not write the final script yet."
	sessionID, canceled := d.runChatTurn(ctx, claudeBin, "", chatDefaultTools, plan, "", send)
	if canceled {
		return
	}

	_ = send(chatServerMsg{Type: "text", Text: "\nWriting the script…\n"})
	format := "Now output ONLY the finished bash script — no preamble, no explanation, no code fence. " + envNote
	var buf strings.Builder
	collect := func(m chatServerMsg) error {
		if m.Type == "text" {
			buf.WriteString(m.Text)
		}
		if m.Type == "error" {
			return send(m)
		}
		return nil
	}
	_, canceled = d.runChatTurn(ctx, claudeBin, "", chatDefaultTools, format, sessionID, collect)
	if canceled {
		return
	}

	script := stripCodeFence(strings.TrimSpace(buf.String()))
	_ = send(chatServerMsg{Type: "draft", Result: script})
}

// stripCodeFence removes a leading/trailing ```bash … ``` fence the model may
// add despite instructions, so the editor gets clean script text.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
