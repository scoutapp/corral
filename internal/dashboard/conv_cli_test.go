//go:build sqlite_fts5

package dashboard

import (
	"testing"

	"github.com/scoutapp/corral/internal/convstore"
)

// TestConversationsCLI smoke-tests the CLI paths against a seeded DB: list,
// grep, show, and chain all run without error over real data.
func TestConversationsCLI(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())

	// Seed a small chain: a global chat that spawned a worker.
	cs, err := convstore.Open()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := cs.StartConversation(convstore.ConvMeta{ConvKey: "global-chat:r", OriginKind: "global-chat", Title: "debug the flaky test"})
	_ = cs.AppendMessage(root, convstore.Message{Role: "user", Type: "text", Text: "the CI is flaky"})
	child, _ := cs.StartConversation(convstore.ConvMeta{ConvKey: "worker:w", OriginKind: "worker", Title: "fix flaky", ParentConversationID: root})
	_ = cs.AppendMessage(child, convstore.Message{Role: "assistant", Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"go test"}`})
	cs.Close()

	// Each invocation should complete without error (output goes to stdout).
	cases := [][]string{
		{},                      // list
		{"--grep", "flaky"},     // search
		{"--origin", "worker"},  // filter
		{"show", itoa64(child)}, // show messages
		{"show", itoa64(root), "--grep", "flaky"}, // in-conv search
		{"chain", itoa64(child)},                  // causal chain
	}
	for _, args := range cases {
		if err := CmdConversations(args); err != nil {
			t.Errorf("CmdConversations(%v): %v", args, err)
		}
	}

	// Bad ids are reported as errors, not panics.
	if err := CmdConversations([]string{"show", "notanumber"}); err == nil {
		t.Error("expected an error for a non-numeric id")
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
