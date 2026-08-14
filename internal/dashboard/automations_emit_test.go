package dashboard

import (
	"context"
	"testing"

	"github.com/scoutapp/corral/internal/automations"
)

// TestPRActionEventMapping locks the action→event mapping the emit sites rely on.
func TestPRActionEventMapping(t *testing.T) {
	cases := map[string]string{
		"approve":         automations.EventPRApprove,
		"comment":         automations.EventPRComment,
		"request-changes": automations.EventPRRequestChanges,
		"merge":           automations.EventPRMerge,
		"bogus":           "",
	}
	for action, want := range cases {
		if got := prActionEvent(action); got != want {
			t.Errorf("prActionEvent(%q) = %q, want %q", action, got, want)
		}
	}
}

// TestEmitNoHooksIsSafe verifies the emit helpers are a safe no-op when nothing
// is configured — the overwhelmingly common path. They must never panic or error
// even for an unknown PR id / workspace (they swallow lookup failures).
func TestEmitNoHooksIsSafe(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")

	// Unknown PR id, no hooks: should return quietly (lookup fails, no run).
	d.firePRHookEvent(context.Background(), 99999, automations.EventPRAnalyze, nil)
	d.firePRHookEvent(context.Background(), 99999, automations.EventPREnter, nil)
	// Unknown workspace: also quiet.
	d.fireProjectStartHooks(context.Background(), "/nonexistent/workspace")

	// No runs should have been recorded (no hooks, and PR lookups failed).
	s, err := d.getStore()
	if err != nil {
		t.Fatalf("getStore: %v", err)
	}
	runs, err := automations.New(s).RecentRuns(10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs from no-op emits, got %d", len(runs))
	}
}
