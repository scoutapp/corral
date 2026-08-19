//go:build sqlite_fts5

// These tests exercise FTS5 MATCH, which is only compiled into the sqlite driver
// under the sqlite_fts5 build tag (the tag corral ships with — see install.sh and
// .goreleaser.*.yml). Guarding the file with the same tag means a tag-less
// `go test ./...` skips it cleanly instead of failing at the MATCH call, while
// `go test -tags sqlite_fts5 ./...` (the real build config) runs it.
package convstore

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *ConvStore {
	t.Helper()
	s, err := openAt(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatalf("openAt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestConversationRoundTrip covers start → append (incl. tool calls) → summary
// rollup → and FTS5 MATCH search (the reason this DB carries the build tag).
func TestConversationRoundTrip(t *testing.T) {
	s := newTestStore(t)

	convID, err := s.StartConversation(ConvMeta{
		ConvKey: "global-chat:tok1", OriginKind: "global-chat",
		TraceID: "trace-abc", Title: "",
	})
	if err != nil || convID == 0 {
		t.Fatalf("StartConversation: id=%d err=%v", convID, err)
	}

	// A user turn, an assistant text, and a tool_use — the typical shapes.
	msgs := []Message{
		{Role: "user", Type: "text", Text: "please grep the migration files"},
		{Role: "assistant", Type: "text", Text: "Looking at the migrations now."},
		{Role: "assistant", Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"grep -r fts5 internal"}`},
		{Role: "user", Type: "tool_result", ToolResult: "0001_conversations.sql: USING fts5"},
		{Role: "assistant", Type: "result", Model: "claude-opus-4-8", CostUSD: "$0.0123"},
	}
	for _, m := range msgs {
		if err := s.AppendMessage(convID, m); err != nil {
			t.Fatalf("AppendMessage %q: %v", m.Type, err)
		}
	}

	// Summary rollup: count, model, first_prompt/title, cost.
	var count int
	var model, firstPrompt, title string
	var cost float64
	if err := s.db.QueryRow(
		`SELECT message_count, COALESCE(model,''), COALESCE(first_prompt,''), COALESCE(title,''), total_cost_usd
		   FROM conversations WHERE id = ?`, convID,
	).Scan(&count, &model, &firstPrompt, &title, &cost); err != nil {
		t.Fatalf("summary select: %v", err)
	}
	if count != len(msgs) {
		t.Errorf("message_count = %d, want %d", count, len(msgs))
	}
	if model != "claude-opus-4-8" {
		t.Errorf("model rollup = %q, want claude-opus-4-8", model)
	}
	if firstPrompt == "" || title == "" {
		t.Errorf("first_prompt/title should default from first user text; got %q / %q", firstPrompt, title)
	}
	if cost < 0.0122 || cost > 0.0124 {
		t.Errorf("cost rollup = %v, want ~0.0123", cost)
	}

	// FTS5 MATCH: "migration" is in the user text; "fts5" is in a tool_result.
	assertFTS := func(query string, wantAtLeast int) {
		t.Helper()
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM conv_messages_fts WHERE conv_messages_fts MATCH ?`, query,
		).Scan(&n); err != nil {
			t.Fatalf("FTS MATCH %q: %v", query, err)
		}
		if n < wantAtLeast {
			t.Errorf("FTS MATCH %q returned %d rows, want >= %d", query, n, wantAtLeast)
		}
	}
	assertFTS("migration", 1) // porter stemming: "migration" ~ "migrations"
	assertFTS("fts5", 1)      // in tool_result
	assertFTS("grep", 1)      // in tool_input

	// Deleting the conversation cascades to messages AND keeps FTS consistent
	// (the AFTER DELETE trigger removes the fts rows).
	if _, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, convID); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE conversation_id = ?`, convID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("messages not cascaded on conversation delete: %d remain", remaining)
	}
	var ftsRemaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM conv_messages_fts WHERE conv_messages_fts MATCH 'migration'`).Scan(&ftsRemaining); err != nil {
		t.Fatal(err)
	}
	if ftsRemaining != 0 {
		t.Errorf("FTS rows not cleaned up after delete: %d remain", ftsRemaining)
	}
}

// TestStartConversationUpsert verifies the conv_key upsert returns the same id
// and promotes the session id (placeholder-key → real-session flow).
func TestStartConversationUpsert(t *testing.T) {
	s := newTestStore(t)
	id1, err := s.StartConversation(ConvMeta{ConvKey: "worker:job-1", OriginKind: "worker", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.StartConversation(ConvMeta{ConvKey: "worker:job-1", OriginKind: "worker", ClaudeSessionID: "sess-9"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("upsert returned a different id: %d vs %d", id1, id2)
	}
	var sess, title string
	if err := s.db.QueryRow(`SELECT COALESCE(claude_session_id,''), COALESCE(title,'') FROM conversations WHERE id = ?`, id1).Scan(&sess, &title); err != nil {
		t.Fatal(err)
	}
	if sess != "sess-9" {
		t.Errorf("session id not promoted on upsert: %q", sess)
	}
	if title != "t" {
		t.Errorf("existing title should be preserved on upsert: %q", title)
	}
}
