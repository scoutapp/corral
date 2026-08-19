package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMergeJobRegistryPersistence covers add → persist → reload, the
// interrupted-on-restart downgrade, and remove cleanup.
func TestMergeJobRegistryPersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)

	reg := newMergeJobRegistry(nil)
	j := &mergeJob{
		ID:          "pr7-abc",
		PRID:        7,
		RepoID:      "repo1",
		PRNumber:    7,
		RepoName:    "acme/widget",
		Strategy:    "squash",
		Status:      mergeJobRunning, // pretend it was mid-run when the daemon stopped
		CreatedAt:   "2026-08-18T00:00:00Z",
		subscribers: map[chan mergeJobEvent]struct{}{},
	}
	reg.add(j)

	// Index file should exist after add (add persists).
	if _, err := os.Stat(mergeJobIndexPath()); err != nil {
		t.Fatalf("expected index.json after add: %v", err)
	}

	// Simulate a fresh dashboard: a new registry loading from disk.
	reg2 := newMergeJobRegistry(nil)
	reg2.load()
	got := reg2.get("pr7-abc")
	if got == nil {
		t.Fatal("job not restored from disk")
	}
	// A job left "running" across a restart must be downgraded to interrupted.
	if got.Status != mergeJobInterrupted {
		t.Fatalf("reloaded running job status = %q, want %q", got.Status, mergeJobInterrupted)
	}
	if got.RepoName != "acme/widget" || got.PRNumber != 7 {
		t.Fatalf("reloaded job metadata mismatch: %+v", got)
	}

	// Write a transcript file, then remove the job — the transcript should go too.
	_ = os.MkdirAll(mergeJobsDir(), 0700)
	tp := transcriptPath("pr7-abc")
	if err := os.WriteFile(tp, []byte(`{"type":"text","text":"hi"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !reg2.remove("pr7-abc") {
		t.Fatal("remove reported job not found")
	}
	if _, err := os.Stat(tp); !os.IsNotExist(err) {
		t.Fatalf("transcript should be removed after remove(); stat err=%v", err)
	}
	if reg2.get("pr7-abc") != nil {
		t.Fatal("job still present after remove")
	}
}

// TestReplayTranscript round-trips events through the on-disk transcript.
func TestReplayTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)
	_ = os.MkdirAll(mergeJobsDir(), 0700)

	j := &mergeJob{ID: "pr3-xyz", subscribers: map[chan mergeJobEvent]struct{}{}}
	j.emit(chatServerMsg{Type: "text", Text: "line one"})
	j.emit(chatServerMsg{Type: "tool_use", Tool: "Bash", Input: `{"command":"git status"}`})

	var got []chatServerMsg
	replayTranscript("pr3-xyz", func(m chatServerMsg) error {
		got = append(got, m)
		return nil
	})
	if len(got) != 2 {
		t.Fatalf("replay got %d events, want 2", len(got))
	}
	if got[0].Text != "line one" || got[1].Tool != "Bash" {
		t.Fatalf("replay content mismatch: %+v", got)
	}

	// Transcript lives under CORRAL_HOME/merge-jobs.
	if _, err := os.Stat(filepath.Join(home, "merge-jobs", "pr3-xyz.jsonl")); err != nil {
		t.Fatalf("expected transcript file: %v", err)
	}
}
