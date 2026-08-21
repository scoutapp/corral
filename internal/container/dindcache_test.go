package container

import (
	"testing"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
)

// TestSharedModeMountsCacheDirectly is a SPEED guard: in shared mode the inner
// docker data root must be the cache volume ITSELF (mounted directly — zero
// copy, the fast path). If this ever routes through the per-workspace volume it
// means a copy-seed, which is the ~15GB tar that made reuse slower than a clean
// build. Locks the fast path so it can't silently regress to copy.
func TestSharedModeMountsCacheDirectly(t *testing.T) {
	cfg := &config.ProjectConfig{Workspace: "/tmp/ws-x"}

	// Shared: must mount the cache volume directly.
	sc := &Corral{dindCache: &config.DindCacheRef{Name: "repo-abc", Mode: config.DindCacheModeShared}}
	if got := sc.dindDataVolume(cfg); got != dindcache.VolumeName("repo-abc") {
		t.Fatalf("shared mode must mount the cache volume directly (zero copy), got %q", got)
	}

	// Copy (and no-cache): the per-workspace volume (seed target / fresh) — NOT the
	// cache volume. This is the slow path, correct only when explicitly chosen.
	scCopy := &Corral{dindCache: &config.DindCacheRef{Name: "repo-abc", Mode: config.DindCacheModeCopy}}
	if got := scCopy.dindDataVolume(cfg); got != config.DindVolumeName(cfg.Workspace) {
		t.Fatalf("copy mode should use the per-workspace volume, got %q", got)
	}
}
