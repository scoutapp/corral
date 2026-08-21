package dashboard

import (
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestProjectRepoID covers the fix for issue/PR/clone projects sharing one repo
// baseline: the repo id resolves from the persisted RepoID (set for ALL repo
// origins), falling back to Source.RepoID for projects created before RepoID was
// recorded. The whole point is that a PR-derived and an issue-derived project of
// the SAME repo resolve to the SAME id → the same repo-<id> baseline.
func TestProjectRepoID(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		if got := projectRepoID(nil); got != "" {
			t.Fatalf("nil cfg → %q, want empty", got)
		}
	})

	t.Run("persisted RepoID wins (any origin)", func(t *testing.T) {
		cfg := &config.ProjectConfig{RepoID: "repo-abc"}
		if got := projectRepoID(cfg); got != "repo-abc" {
			t.Fatalf("got %q, want repo-abc", got)
		}
	})

	t.Run("issue-derived project keys on RepoID, not Source kind", func(t *testing.T) {
		cfg := &config.ProjectConfig{
			RepoID: "repo-abc",
			Source: &config.ProjectSource{Kind: "issue", RepoID: "repo-abc", Number: 123},
		}
		if got := projectRepoID(cfg); got != "repo-abc" {
			t.Fatalf("issue-derived → %q, want repo-abc", got)
		}
	})

	t.Run("PR-derived and issue-derived of same repo resolve identically", func(t *testing.T) {
		pr := &config.ProjectConfig{RepoID: "repo-x", Source: &config.ProjectSource{Kind: "pr", RepoID: "repo-x", Number: 5670}}
		iss := &config.ProjectConfig{RepoID: "repo-x", Source: &config.ProjectSource{Kind: "issue", RepoID: "repo-x", Number: 42}}
		if projectRepoID(pr) != projectRepoID(iss) {
			t.Fatalf("same-repo PR vs issue must share a baseline: %q != %q", projectRepoID(pr), projectRepoID(iss))
		}
	})

	t.Run("fallback to Source for old projects without RepoID", func(t *testing.T) {
		cfg := &config.ProjectConfig{Source: &config.ProjectSource{Kind: "pr", RepoID: "repo-legacy"}}
		if got := projectRepoID(cfg); got != "repo-legacy" {
			t.Fatalf("legacy fallback → %q, want repo-legacy", got)
		}
	})

	t.Run("plain clone with neither → empty (non-repo project)", func(t *testing.T) {
		if got := projectRepoID(&config.ProjectConfig{}); got != "" {
			t.Fatalf("no repo → %q, want empty", got)
		}
	})
}
