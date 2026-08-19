package dashboard

import (
	"errors"
	"testing"
)

// TestAnalysisJobTracker covers the fire-and-return state machine: default idle,
// begin→running (and the double-start guard), and finish→done/failed.
func TestAnalysisJobTracker(t *testing.T) {
	tr := newAnalysisJobTracker()

	// Default is idle.
	if s := tr.state(1, "enrich"); s.Status != analysisIdle {
		t.Fatalf("fresh job: want idle, got %q", s.Status)
	}

	// begin → running; a second begin while running is refused (no double-spawn).
	if !tr.begin(1, "enrich") {
		t.Fatal("first begin should succeed")
	}
	if tr.begin(1, "enrich") {
		t.Fatal("second begin while running should be refused")
	}
	if s := tr.state(1, "enrich"); s.Status != analysisRunning {
		t.Fatalf("after begin: want running, got %q", s.Status)
	}

	// Different kind is independent.
	if !tr.begin(1, "risk") {
		t.Fatal("begin for a different kind should succeed independently")
	}

	// finish with error → failed + message.
	tr.finish(1, "enrich", errors.New("boom"))
	if s := tr.state(1, "enrich"); s.Status != analysisFailed || s.Error != "boom" {
		t.Fatalf("after failed finish: %+v", s)
	}

	// After finishing, begin can run again (re-analyze).
	if !tr.begin(1, "enrich") {
		t.Fatal("begin after finish should succeed")
	}
	tr.finish(1, "enrich", nil)
	if s := tr.state(1, "enrich"); s.Status != analysisDone || s.Error != "" {
		t.Fatalf("after ok finish: %+v", s)
	}

	// The other kind is still running (independent state).
	if s := tr.state(1, "risk"); s.Status != analysisRunning {
		t.Fatalf("risk should still be running, got %q", s.Status)
	}
}
