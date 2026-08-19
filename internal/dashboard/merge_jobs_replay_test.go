//go:build sqlite_fts5

package dashboard

import (
	"context"
	"testing"
)

// TestReplayJobFromDB verifies a viewer replays a job's history from the
// conversations DB (the source of truth) — not a JSONL file, which no longer
// exists. Guarded by the FTS tag because it opens convstore.
func TestReplayJobFromDB(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	// Capture a couple frames into a conversation via the tee, then point a job at
	// that conversation id (mirrors what runWorkerJob/runMergeJob do).
	capt, send, finalize := d.captureSend(context.Background(),
		convOrigin{Kind: jobKindWorker, OriginID: "job-x"}, func(chatServerMsg) error { return nil })
	if capt == nil {
		t.Fatal("capture unavailable")
	}
	send(chatServerMsg{Type: "text", Text: "line one"})
	send(chatServerMsg{Type: "tool_use", Tool: "Bash", Input: `{"command":"git status"}`})
	finalize("done")

	job := &mergeJob{ID: "job-x", ConvID: capt.ConvID(), subscribers: map[chan mergeJobEvent]struct{}{}}

	var got []chatServerMsg
	d.replayJob(job, func(m chatServerMsg) error {
		got = append(got, m)
		return nil
	})
	if len(got) != 2 {
		t.Fatalf("replay got %d events, want 2", len(got))
	}
	if got[0].Text != "line one" || got[1].Tool != "Bash" {
		t.Fatalf("replay content mismatch: %+v", got)
	}
}
