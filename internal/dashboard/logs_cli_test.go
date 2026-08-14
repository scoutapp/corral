package dashboard

import (
	"os"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/store"
)

// TestCmdLogs seeds a couple of rows then runs `corral logs` variants, capturing
// stdout to assert the output shape + filters.
func TestCmdLogs(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	s, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	lg := applog.New(s, false)
	lg.Info(applog.CatAI, "ai.analyze", "Analyzed PR #7", nil)
	lg.Errorf(applog.CatPRAction, "pr.merge", "Merge failed for PR #7", os.ErrClosed, nil)
	s.Close()

	out := captureStdout(t, func() {
		if err := CmdLogs([]string{"--limit", "50"}); err != nil {
			t.Fatalf("CmdLogs: %v", err)
		}
	})
	// Oldest-first (chronological); both lines present.
	if !strings.Contains(out, "Analyzed PR #7") || !strings.Contains(out, "Merge failed") {
		t.Errorf("plain output missing entries:\n%s", out)
	}
	if !strings.Contains(out, "[ai]") || !strings.Contains(out, "ERROR") {
		t.Errorf("plain output missing category/level:\n%s", out)
	}

	// Filter by category.
	out = captureStdout(t, func() { _ = CmdLogs([]string{"--category", "ai"}) })
	if strings.Contains(out, "Merge failed") || !strings.Contains(out, "Analyzed PR #7") {
		t.Errorf("category filter wrong:\n%s", out)
	}

	// grep.
	out = captureStdout(t, func() { _ = CmdLogs([]string{"--grep", "Merge"}) })
	if !strings.Contains(out, "Merge failed") || strings.Contains(out, "Analyzed") {
		t.Errorf("grep wrong:\n%s", out)
	}

	// JSON output — one object per line.
	out = captureStdout(t, func() { _ = CmdLogs([]string{"--json"}) })
	if !strings.Contains(out, `"category":"ai"`) || !strings.Contains(out, `"event":"pr.merge"`) {
		t.Errorf("json output wrong:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wp
	fn()
	wp.Close()
	os.Stdout = old
	buf := make([]byte, 64*1024)
	n, _ := rp.Read(buf)
	rp.Close()
	return string(buf[:n])
}
