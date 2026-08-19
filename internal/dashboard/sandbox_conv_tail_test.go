//go:build sqlite_fts5

package dashboard

import (
	"testing"
)

// TestClaudeProjectSlug matches Claude Code's session-dir naming.
func TestClaudeProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/jack/claude_sandbox": "-Users-jack-claude-sandbox",
		"/Users/jack/.corral":        "-Users-jack--corral", // '/.' → '--'
	}
	for in, want := range cases {
		if got := claudeProjectSlug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIngestSandboxLine parses Claude Code session records (the real shapes,
// validated against on-disk sessions) into conv_messages under one sandbox
// conversation per session id, separating tool calls.
func TestIngestSandboxLine(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	cs, err := d.getConvStore()
	if err != nil {
		t.Fatal(err)
	}
	st := &sandboxTailState{offsets: map[string]int64{}, convIDs: map[string]int64{}}

	lines := []string{
		// user text (content as a plain string)
		`{"type":"user","sessionId":"S1","message":{"role":"user","content":"fix the failing test"}}`,
		// assistant text + tool_use in one record (content as an array)
		`{"type":"assistant","sessionId":"S1","message":{"role":"assistant","content":[{"type":"text","text":"Running the tests."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		// tool_result (content as a string)
		`{"type":"user","sessionId":"S1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"FAIL: TestFoo"}]}}`,
		// a non-conversational record → skipped
		`{"type":"file-history-snapshot","sessionId":"S1"}`,
	}
	for _, l := range lines {
		d.ingestSandboxLine(cs, st, "/Users/jack/proj", []byte(l))
	}

	// One conversation (sandbox origin), four content messages (text, text,
	// tool_use, tool_result — the snapshot is skipped).
	var convCount, msgCount int
	var origin string
	cs.DB().QueryRow(`SELECT COUNT(*), COALESCE(MAX(origin_kind),'') FROM conversations`).Scan(&convCount, &origin)
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages`).Scan(&msgCount)
	if convCount != 1 || origin != "sandbox" {
		t.Fatalf("expected 1 sandbox conversation, got count=%d origin=%q", convCount, origin)
	}
	if msgCount != 4 {
		t.Fatalf("expected 4 messages, got %d", msgCount)
	}

	// Tool call captured with name + input; tool_result captured.
	var tools, results int
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE type='tool_use' AND tool_name='Bash'`).Scan(&tools)
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE type='tool_result'`).Scan(&results)
	if tools != 1 || results != 1 {
		t.Fatalf("tool capture wrong: tool_use=%d tool_result=%d", tools, results)
	}

	// A second session id makes a second conversation.
	d.ingestSandboxLine(cs, st, "/Users/jack/proj",
		[]byte(`{"type":"user","sessionId":"S2","message":{"role":"user","content":"another session"}}`))
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convCount)
	if convCount != 2 {
		t.Fatalf("second session should create a second conversation, got %d", convCount)
	}
}
