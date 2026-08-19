//go:build sqlite_fts5

package dashboard

import (
	"context"
	"errors"
	"testing"
)

// fakeRunner is a stand-in aiRunner returning a canned reply (or error).
type fakeRunner struct {
	reply string
	err   error
	calls int
}

func (f *fakeRunner) Run(_ context.Context, _ string) (string, error) {
	f.calls++
	return f.reply, f.err
}

// TestCapturingRunnerRecordsPairs verifies multiple Runs land as prompt/response
// message pairs in ONE conversation (so a whole enrich run is one conversation),
// and that the wrapped result passes through unchanged.
func TestCapturingRunnerRecordsPairs(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	inner := &fakeRunner{reply: `{"title":"ok"}`}
	r := d.capturingRunner(context.Background(),
		convOrigin{Kind: "analysis", OriginID: "enrich-1", PRNumber: 1}, inner)

	// Two Runs (as an enrich would do: block + summary).
	for i := 0; i < 2; i++ {
		out, err := r.Run(context.Background(), "analyze this")
		if err != nil || out != inner.reply {
			t.Fatalf("Run passthrough failed: out=%q err=%v", out, err)
		}
	}

	cs, err := d.getConvStore()
	if err != nil {
		t.Fatal(err)
	}
	var convCount, msgCount int
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&convCount)
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages`).Scan(&msgCount)
	if convCount != 1 {
		t.Fatalf("expected 1 conversation for the runner, got %d", convCount)
	}
	if msgCount != 4 { // 2 runs × (user prompt + assistant reply)
		t.Fatalf("expected 4 messages (2 prompt/response pairs), got %d", msgCount)
	}
}

// TestCapturingRunnerRecordsError verifies a failed Run records an error message
// and still returns the error to the caller.
func TestCapturingRunnerRecordsError(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	boom := errors.New("cli exploded")
	r := d.capturingRunner(context.Background(),
		convOrigin{Kind: "analysis", OriginID: "risk-2"}, &fakeRunner{err: boom})

	if _, err := r.Run(context.Background(), "assess risk"); !errors.Is(err, boom) {
		t.Fatalf("error should pass through: %v", err)
	}
	cs, _ := d.getConvStore()
	var errCount int
	cs.DB().QueryRow(`SELECT COUNT(*) FROM conv_messages WHERE is_error = 1`).Scan(&errCount)
	if errCount != 1 {
		t.Fatalf("expected 1 error message recorded, got %d", errCount)
	}
}

// TestCapturingRunnerNilPassthrough: a nil inner runner (no claude) is returned
// unchanged so the analysis path behaves exactly as before.
func TestCapturingRunnerNilPassthrough(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	if r := d.capturingRunner(context.Background(), convOrigin{Kind: "analysis"}, nil); r != nil {
		t.Fatalf("nil inner runner should stay nil, got %T", r)
	}
}
