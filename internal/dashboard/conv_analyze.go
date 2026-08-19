package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/convstore"
)

// handleConversationsAnalyze runs an AI analysis OF captured logs/conversations,
// and captures that analysis as its own conversation (recursive). It gathers the
// target's transcript into a prompt and spawns a log-analysis worker (reusing the
// conductor-worker machinery) whose conversation records parent_conversation_id =
// the analyzed conversation — so the analysis joins the causal chain and streams
// in the Work tab like any other worker.
//
//	POST /api/conversations/analyze
//	  { "conversationId": <id>, "question"?: "..." }
//
// This is a mutating call (spawns host Claude), so it's subject to the API-writes
// gate in handleRoot when driven by the API token.
func (d *dashboardServer) handleConversationsAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ConversationID int64  `json:"conversationId"`
		Question       string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.ConversationID <= 0 {
		http.Error(w, "conversationId is required", http.StatusBadRequest)
		return
	}
	cs, err := d.getConvStore()
	if err != nil {
		http.Error(w, "conversations database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	conv, err := cs.Get(body.ConversationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	msgs, err := cs.Messages(body.ConversationID, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	question := strings.TrimSpace(body.Question)
	if question == "" {
		question = "Summarize what happened in this conversation, call out anything that failed or looks risky, and suggest next steps."
	}
	prompt := buildLogAnalysisPrompt(conv.OriginKind, conv.Title, msgs, question)

	title := "Analyze: " + firstNonEmpty(conv.Title, conv.OriginKind)
	job, err := d.startWorkerJob(prompt, truncate(title, 60), body.ConversationID, "log-analysis")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeFilesJSON(w, map[string]any{"jobId": job.ID, "analyzing": body.ConversationID})
}

// buildLogAnalysisPrompt renders a conversation's transcript into a prompt for a
// fresh Claude to analyze. It's plain text (no tools needed to read it — the
// transcript is inline), bounded so a huge conversation doesn't overflow.
func buildLogAnalysisPrompt(origin, title string, msgs []convstore.MessageRow, question string) string {
	var b strings.Builder
	b.WriteString("You are analyzing a captured Claude conversation from the Corral app.\n")
	b.WriteString(fmt.Sprintf("Origin: %s\n", origin))
	if title != "" {
		b.WriteString(fmt.Sprintf("Title: %s\n", title))
	}
	b.WriteString("\n--- transcript ---\n")
	const maxPromptBytes = 24000 // keep the prompt bounded
	for _, m := range msgs {
		line := renderAnalysisLine(m)
		if b.Len()+len(line) > maxPromptBytes {
			b.WriteString("…(transcript truncated)\n")
			break
		}
		b.WriteString(line)
	}
	b.WriteString("--- end transcript ---\n\n")
	b.WriteString(question)
	return b.String()
}

// renderAnalysisLine formats one message for the transcript block.
func renderAnalysisLine(m convstore.MessageRow) string {
	switch m.Type {
	case "tool_use":
		return fmt.Sprintf("[%s] tool %s: %s\n", m.Role, m.ToolName, truncate(m.ToolInput, 300))
	case "tool_result":
		return fmt.Sprintf("[%s] result: %s\n", m.Role, truncate(m.ToolResult, 500))
	default:
		if strings.TrimSpace(m.Text) == "" {
			return ""
		}
		return fmt.Sprintf("[%s] %s\n", m.Role, m.Text)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
