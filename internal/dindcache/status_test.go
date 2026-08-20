package dindcache

import (
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestComputeStatus covers the reuse verdict across the cache states the banner
// shows: no cache, copy seeded vs not-yet, shared, repo vs hand-named, and a
// missing cache. cacheVolExists/projectVolExists are injected (no docker).
func TestComputeStatus(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	repoRef := &config.DindCacheRef{Name: "repo-abc123", Mode: config.DindCacheModeCopy}
	sharedRepoRef := &config.DindCacheRef{Name: "repo-abc123", Mode: config.DindCacheModeShared}
	namedRef := &config.DindCacheRef{Name: "my-baseline", Mode: config.DindCacheModeCopy}

	t.Run("no cache attached", func(t *testing.T) {
		s := ComputeStatus(nil, "corral-dind-x", yes, yes)
		if s.Reused || s.CacheName != "" {
			t.Fatalf("expected not-reused, no cache: %+v", s)
		}
	})

	t.Run("copy seeded → reused", func(t *testing.T) {
		s := ComputeStatus(repoRef, "corral-dind-x", yes /*cache exists*/, yes /*project vol exists*/)
		if !s.Reused || !s.IsRepo || s.Mode != config.DindCacheModeCopy {
			t.Fatalf("expected reused repo copy: %+v", s)
		}
		if !strings.Contains(s.Reason, "Reusing") {
			t.Fatalf("reason: %q", s.Reason)
		}
	})

	t.Run("copy not yet seeded → not reused (will seed)", func(t *testing.T) {
		s := ComputeStatus(repoRef, "corral-dind-x", yes /*cache exists*/, no /*project vol absent*/)
		if s.Reused {
			t.Fatalf("expected not-yet-reused: %+v", s)
		}
		if !strings.Contains(s.Reason, "Will seed") {
			t.Fatalf("reason: %q", s.Reason)
		}
	})

	t.Run("shared → reused when cache exists", func(t *testing.T) {
		s := ComputeStatus(sharedRepoRef, "corral-dind-x", yes, no /*project vol irrelevant in shared*/)
		if !s.Reused || s.Mode != config.DindCacheModeShared {
			t.Fatalf("expected reused shared: %+v", s)
		}
	})

	t.Run("repo cache missing → not reused, 'no baseline'", func(t *testing.T) {
		s := ComputeStatus(repoRef, "corral-dind-x", no /*cache absent*/, no)
		if s.Reused {
			t.Fatalf("expected not-reused: %+v", s)
		}
		if !strings.Contains(strings.ToLower(s.Reason), "baseline") {
			t.Fatalf("reason should mention baseline: %q", s.Reason)
		}
	})

	t.Run("hand-named cache is not flagged as repo", func(t *testing.T) {
		s := ComputeStatus(namedRef, "corral-dind-x", yes, yes)
		if s.IsRepo {
			t.Fatalf("my-baseline should not be a repo cache: %+v", s)
		}
	})
}
