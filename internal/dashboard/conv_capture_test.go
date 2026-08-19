//go:build sqlite_fts5

// Capture tests open the conversations DB, whose FTS5 virtual table only compiles
// under the sqlite_fts5 tag (corral's real build). Tag-less, getConvStore fails
// and capture degrades to passthrough — chat still works, just uncaptured — so
// these assertions (which expect a live capturer) run only under the tag.
package dashboard

import (
	"context"
	"errors"
	"testing"
)

// TestCaptureSendRecordsAndPassesThrough drives the capture tee: it must forward
// every frame to the underlying send (returning its error verbatim) AND persist
// the conversation + messages, without the DB affecting the stream.
func TestCaptureSendRecordsAndPassesThrough(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	var delivered []chatServerMsg
	underlying := func(m chatServerMsg) error {
		delivered = append(delivered, m)
		return nil
	}

	cap, send, finalize := d.captureSend(context.Background(),
		convOrigin{Kind: "global-chat"}, underlying)
	if cap == nil {
		t.Fatal("expected a capturer (convstore should open under temp CORRAL_HOME)")
	}

	cap.recordPrompt("find the bug")
	frames := []chatServerMsg{
		{Type: "session", SessionID: "sess-1"},
		{Type: "text", Text: "Looking..."},
		{Type: "tool_use", Tool: "Bash", Input: `{"command":"go test"}`},
		{Type: "tool_result", Result: "ok"},
		{Type: "result", Model: "claude-opus-4-8", CostUSD: "$0.01"},
		{Type: "turn_end"},
	}
	for _, f := range frames {
		if err := send(f); err != nil {
			t.Fatalf("send returned error: %v", err)
		}
	}
	finalize("done")

	// Every frame reached the browser, in order.
	if len(delivered) != len(frames) {
		t.Fatalf("delivered %d frames, want %d", len(delivered), len(frames))
	}

	// The conversation + messages persisted. Prompt(user) + text + tool_use +
	// tool_result + result = 5 messages (session/turn_end are not messages).
	cs, err := d.getConvStore()
	if err != nil {
		t.Fatalf("getConvStore: %v", err)
	}
	var convCount, msgCount int
	var sessionID, status string
	if err := cs.DB().QueryRow(`SELECT COUNT(*), COALESCE(MAX(claude_session_id),''), COALESCE(MAX(status),'') FROM conversations`).
		Scan(&convCount, &sessionID, &status); err != nil {
		t.Fatal(err)
	}
	if convCount != 1 {
		t.Fatalf("expected 1 conversation, got %d", convCount)
	}
	if sessionID != "sess-1" {
		t.Errorf("session id not promoted: %q", sessionID)
	}
	if status != "done" {
		t.Errorf("finalize status = %q, want done", status)
	}
	if err := cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages`).Scan(&msgCount); err != nil {
		t.Fatal(err)
	}
	if msgCount != 5 {
		t.Errorf("expected 5 messages, got %d", msgCount)
	}

	// The prompt is a user-role text message; the assistant "text" frame is
	// assistant-role — confirm both roles landed (recordPrompt vs the mapper).
	var userText, assistantText int
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE role='user' AND type='text'`).Scan(&userText)
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE role='assistant' AND type='text'`).Scan(&assistantText)
	if userText != 1 || assistantText != 1 {
		t.Errorf("role split wrong: user-text=%d assistant-text=%d", userText, assistantText)
	}
}

// TestCaptureSendStreamUnaffectedByDelivery confirms the tee returns the
// underlying send's error unchanged (capture is a pure side effect).
func TestCaptureSendStreamUnaffectedByDelivery(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	boom := errors.New("socket closed")
	_, send, _ := d.captureSend(context.Background(), convOrigin{Kind: "global-chat"},
		func(chatServerMsg) error { return boom })

	if err := send(chatServerMsg{Type: "text", Text: "hi"}); !errors.Is(err, boom) {
		t.Fatalf("send should return the underlying error verbatim, got %v", err)
	}
}
