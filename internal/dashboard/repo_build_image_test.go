package dashboard

import (
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/repos"
)

// TestBuildImagePrompt checks the build recipe carries the right repo id, the
// create/snapshot/teardown steps, the baseline cache name, and honors an explicit
// command / image tag.
func TestBuildImagePrompt(t *testing.T) {
	repo := &repos.Repo{ID: "abc123", Name: "acme-web", DefaultBranch: "main"}

	// Inferred build (no command).
	got := buildImagePrompt(repo, "", "")
	for _, want := range []string{
		"acme-web", "abc123",
		"/projects/create",                 // creates a throwaway project
		"/api/dind/caches",                 // snapshots
		"repo-abc123",                      // the baseline cache name
		"DELETE /projects/",                // tears down
		"Infer the build",                  // inference guidance when no command
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inferred prompt missing %q:\n%s", want, got)
		}
	}

	// Explicit command + tag.
	got = buildImagePrompt(repo, "make docker-image", "acme-web:latest")
	if !strings.Contains(got, "make docker-image") {
		t.Errorf("explicit command not used: %s", got)
	}
	if !strings.Contains(got, "acme-web:latest") {
		t.Errorf("image tag not used: %s", got)
	}
	if strings.Contains(got, "Infer the build") {
		t.Errorf("explicit command should not include inference guidance: %s", got)
	}
}
