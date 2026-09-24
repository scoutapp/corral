package dashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newestTranscriptMtime picks the freshest .jsonl and ignores non-transcripts.
func TestNewestTranscriptMtime(t *testing.T) {
	dir := t.TempDir()

	// Absent dir → not found.
	if _, ok := newestTranscriptMtime(filepath.Join(dir, "nope")); ok {
		t.Fatal("expected not-found for absent dir")
	}
	// Empty dir → not found.
	if _, ok := newestTranscriptMtime(dir); ok {
		t.Fatal("expected not-found for empty dir")
	}

	old := filepath.Join(dir, "a.jsonl")
	newer := filepath.Join(dir, "b.jsonl")
	noise := filepath.Join(dir, "notes.txt")
	for _, p := range []string{old, newer, noise} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	os.Chtimes(old, base, base)
	os.Chtimes(newer, base.Add(30*time.Minute), base.Add(30*time.Minute))
	// Make the .txt the newest of all — it must still be ignored.
	os.Chtimes(noise, base.Add(50*time.Minute), base.Add(50*time.Minute))

	mt, ok := newestTranscriptMtime(dir)
	if !ok {
		t.Fatal("expected a transcript mtime")
	}
	if want := base.Add(30 * time.Minute); !mt.Equal(want) {
		t.Fatalf("newest mtime = %v, want %v (the .txt must be ignored)", mt, want)
	}
}

// hostTerminalActivity maps transcript freshness to working/waiting, and returns
// "" (→ caller shows "off") when no host -claude session is live. We can't easily
// fake a live tmux session in a unit test, so this covers the no-session path;
// the freshness mapping is exercised via newestTranscriptMtime above.
func TestHostTerminalActivityNoSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CORRAL_HOME", filepath.Join(home, ".corral"))

	// A workspace with no running tmux session must yield "" so projectActivity
	// falls through to "off".
	if got := hostTerminalActivity("/tmp/definitely-not-a-live-project"); got != "" {
		t.Fatalf("hostTerminalActivity with no live session = %q, want \"\"", got)
	}
}
