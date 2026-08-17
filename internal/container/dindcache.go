package container

import (
	"fmt"
	"log"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
)

// dindDataVolume returns the docker volume this project mounts as its inner
// Docker data root. In SHARED cache mode that's the cache volume itself (changes
// persist into the cache); otherwise it's the deterministic per-workspace volume
// (copy mode seeds this volume from the cache; no-cache uses it empty).
func (sc *Corral) dindDataVolume(cfg *config.ProjectConfig) string {
	if sc.dindCache.IsShared() && dindcache.ValidName(sc.dindCache.Name) {
		return dindcache.VolumeName(sc.dindCache.Name)
	}
	return config.DindVolumeName(cfg.Workspace)
}

// seedDindCache performs COPY-mode seeding: on a project's FIRST start with a
// copy-mode cache, it fills the (not-yet-existing) per-workspace volume with a
// full copy of the cache. On later starts the per-workspace volume already
// exists and has diverged, so we leave it alone — re-seeding would clobber the
// project's own accumulated state.
//
// Shared mode is a no-op here: the project mounts the cache volume directly (see
// dindDataVolume), so there's nothing to copy. No-cache is a no-op too.
func (sc *Corral) seedDindCache(cfg *config.ProjectConfig) error {
	ref := sc.dindCache
	if ref == nil || ref.Name == "" {
		return nil
	}
	if !dindcache.ValidName(ref.Name) {
		return fmt.Errorf("invalid DinD cache name %q in project config", ref.Name)
	}
	if !dindcache.Exists(ref.Name) {
		return fmt.Errorf("DinD cache %q not found — create it (Save as cache) or remove it from this project's config", ref.Name)
	}
	if ref.IsShared() {
		// Direct mount; nothing to seed. Warn once if the cache is somehow gone
		// is already handled by the Exists check above.
		log.Printf("DinD cache: sharing volume %s (changes persist to the cache)", dindcache.VolumeName(ref.Name))
		return nil
	}

	// COPY mode. Seed only if the per-workspace volume doesn't exist yet — i.e.
	// this is the first start. If it already exists the project has its own state.
	projVol := config.DindVolumeName(cfg.Workspace)
	if dindcache.VolumeExists(projVol) {
		config.Debugf("DinD cache: project volume %s already exists; skipping seed", projVol)
		return nil
	}
	log.Printf("DinD cache: seeding %s from cache %q (one-time full copy — may take a while)…", projVol, ref.Name)
	if err := dindcache.SeedInto(ref.Name, projVol); err != nil {
		return fmt.Errorf("seed DinD cache: %w", err)
	}
	log.Printf("DinD cache: seed complete")
	return nil
}
