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
// AI-assisted project-start prompt drafting.
//
// WS /api/prompts/draft?repoId=<id>   client sends { "description": "..." }
//
// Mirrors the issue-draft flow: runs the operator's HOST `claude` (NOT
// sandboxed — same read-only Read/Grep/Glob tool set as the chat panel) inside a
// fresh TEMP checkout of the repo mirror, so it can ground the prompt in the
// actual codebase. Two turns:
//   1. research (streamed to the browser), then
//   2. a resumed turn that outputs ONLY the finished prompt TEMPLATE text.
//
// The result is a reusable template that may contain {{repo}}, {{branch}},
// {{pr_number}}, {{pr_title}}, {{pr_url}} placeholders. Drafting NEVER saves the
// prompt — the user reviews it and chooses to use/save it. The temp checkout is
// removed when the socket closes.
//
// repoId is optional: with no repo the draft is generic (no checkout, no
// research), still useful for a global default prompt.
// ----------------------------------------------------------------------------

func (d *dashboardServer) handlePromptDraftWS(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repoId")
	if repoID != "" {
		if _, err := repos.Get(repoID); err != nil {
			http.NotFound(w, r)
			return
		}
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
	capt, send, finalize := d.captureSend(context.Background(), convOrigin{Kind: "prompt-draft", RepoID: repoID}, rawSend)
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

	// The placeholder guidance is appended to both turns so the model knows the
	// template variables it may use.
	const vars = "The prompt is a reusable TEMPLATE. You may include these placeholders, which are " +
		"substituted at launch: {{repo}}, {{branch}}, {{pr_number}}, {{pr_title}}, {{pr_url}}. " +
		"Use them where a concrete value belongs so the template works for any PR/repo."

	checkout := ""
	if repoID != "" {
		// Fresh temp checkout so claude can research the code.
		tmpRoot := filepath.Join(config.CorralHome(), "tmp")
		_ = os.MkdirAll(tmpRoot, 0755)
		tmp, terr := os.MkdirTemp(tmpRoot, "prompt-draft-*")
		if terr != nil {
			tmp, terr = os.MkdirTemp("", "prompt-draft-*")
		}
		if terr == nil {
			defer os.RemoveAll(tmp)
			checkout = filepath.Join(tmp, "repo")
			_ = send(chatServerMsg{Type: "text", Text: "Checking out the repo to research…\n"})
			if err := repos.CloneLocal(repoID, checkout, ""); err != nil {
				_ = send(chatServerMsg{Type: "error", Text: "checkout failed: " + err.Error()})
				checkout = "" // fall back to a generic draft rather than aborting
			}
		}
	}

	// Turn 1 — research (only meaningful with a checkout; harmless without).
	research := "You are writing a project-start prompt: the first instruction given to Claude Code when it " +
		"opens a sandboxed checkout of a repository to work on it. " + vars + "\n\n" +
		"The user wants a prompt that: " + in.Description + "\n\n"
	if checkout != "" {
		research += "Research this codebase (read key files, grep, explore structure) so the prompt is concrete and " +
			"grounded in how this project actually works. Then briefly summarize what you found. Do not write the final prompt yet."
	} else {
		research += "Briefly outline what an effective prompt should cover. Do not write the final prompt yet."
	}
	sessionID, canceled := d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, research, "", send)
	if canceled {
		return
	}

	// Turn 2 — emit ONLY the finished template text (resumed, keeps research).
	_ = send(chatServerMsg{Type: "text", Text: "\nWriting the prompt…\n"})
	format := "Now output ONLY the finished project-start prompt template — the prompt text itself, no preamble, " +
		"no code fence, no explanation. " + vars
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
	_, canceled = d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, format, sessionID, collect)
	if canceled {
		return
	}

	// Send the drafted template back to fill the editor. Reuse the "draft"
	// frame; Result carries the template (Text left empty — no title concept).
	_ = send(chatServerMsg{Type: "draft", Result: strings.TrimSpace(buf.String())})
}
