package dashboard

import (
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/automations"
)

// TestRepoWorkerGuidance covers the per-repo worker guidance: a repo's saved
// agent context is appended to the worker prompt (so a repo carries its own
// boot/caching recipe on top of the generic contract), and it's empty when
// there's no repo or no saved context.
func TestRepoWorkerGuidance(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	// No repo id → no guidance.
	if got := d.repoWorkerGuidance(""); got != "" {
		t.Fatalf("empty repoId should yield no guidance, got %q", got)
	}
	// Repo with no saved context → no guidance.
	if got := d.repoWorkerGuidance("repo-unknown"); got != "" {
		t.Fatalf("repo without context should yield no guidance, got %q", got)
	}

	// Save a repo's agent context, then it should appear (wrapped) in the guidance.
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	svc := automations.New(s)
	if err := svc.SetRepoAgentContext("repo-apm", "Cache gems in apm-bundle; migrate into apm-pgdata."); err != nil {
		t.Fatal(err)
	}
	got := d.repoWorkerGuidance("repo-apm")
	if !strings.Contains(got, "apm-bundle") || !strings.Contains(got, "apm-pgdata") {
		t.Fatalf("guidance should embed the repo's saved context, got %q", got)
	}
	if !strings.Contains(got, "REPO-SPECIFIC GUIDANCE") {
		t.Fatalf("guidance should be labeled as repo-specific, got %q", got)
	}
}
