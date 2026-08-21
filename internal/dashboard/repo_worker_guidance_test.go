package dashboard

import (
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/automations"
)

// TestRepoWorkerGuidance covers the boot/caching guidance now sourced from the
// editable `worker.boot_guidance` catalog prompt: every worker gets the generic
// default, and a repo's saved override replaces it for that repo only.
func TestRepoWorkerGuidance(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	// No repo id → the generic built-in default (not empty).
	if base := d.repoWorkerGuidance(""); !strings.Contains(base, "MAKE EXPENSIVE WORK REUSABLE") {
		t.Fatalf("empty repoId should yield the generic default guidance, got %q", base)
	}
	// A repo with no override → still the generic default.
	if got := d.repoWorkerGuidance("repo-none"); !strings.Contains(got, "MAKE EXPENSIVE WORK REUSABLE") {
		t.Fatalf("repo without override should get the default, got %q", got)
	}

	// Save a repo override in the Prompts catalog → it replaces the default.
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	svc := automations.New(s)
	if _, err := svc.SetPromptOverride(automations.PromptWorkerBoot, "repo-apm",
		"APM RECIPE: gems in apm-deps, DB in apm-db, commit apm-prepared:latest."); err != nil {
		t.Fatalf("SetPromptOverride: %v", err)
	}
	got := d.repoWorkerGuidance("repo-apm")
	if !strings.Contains(got, "APM RECIPE") || !strings.Contains(got, "apm-deps") {
		t.Fatalf("repo override should replace the default, got %q", got)
	}
	if strings.Contains(got, "MAKE EXPENSIVE WORK REUSABLE") {
		t.Fatalf("override should REPLACE the default, not append to it: %q", got)
	}
	// Override is scoped to repo-apm only; others still get the default.
	if other := d.repoWorkerGuidance("repo-other"); strings.Contains(other, "APM RECIPE") {
		t.Fatalf("override must be scoped to repo-apm only, got %q", other)
	}
}
