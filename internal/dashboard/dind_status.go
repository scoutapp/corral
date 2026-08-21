package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
	"github.com/scoutapp/corral/internal/session"
)

// handleDindStatus reports a project's DinD cache reuse status for the
// project-page info banner: which cache is attached, its mode, and whether the
// project is actually starting FROM it (seeded copy / mounted shared) vs. a fresh
// empty inner Docker. Cheap — it reads the project config + docker volume
// existence, with NO inner-docker exec.
//
//	GET /p/<id>/dind/status -> dindcache.Status
func (d *dashboardServer) handleDindStatus(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// DinD off → no inner Docker at all; say so plainly.
	if !cfg.DindEnabled {
		writeFilesJSON(w, dindcache.Status{Reason: "Docker-in-Docker is off for this project."})
		return
	}
	status := dindcache.ComputeStatus(
		cfg.DindCache,
		config.DindVolumeName(workspace),
		dindcache.VolumeExists,
		dindcache.VolumeExists,
	)
	// For a repo-derived project with no baseline yet, set the expectation that one
	// auto-saves on clean stop (so the next project from this repo reuses it) — the
	// banner otherwise reads as a dead-end "starting fresh".
	if repoID := projectRepoID(cfg); !status.Reused && cfg.DindEnabled && repoID != "" {
		if !dindcache.Exists(dindcache.RepoCacheName(repoID)) {
			status.Reason = "No repo baseline yet — this project's build auto-saves as the baseline when you stop it, so the next project from this repo reuses it."
		}
	}
	// LIVE verification: "reused" from ComputeStatus only means a ref/volume exists.
	// It was misleadingly true when a copy seed was still running or failed and the
	// inner docker was actually empty. When we claim reuse and the container is up,
	// confirm the inner docker really holds images from the baseline; downgrade the
	// verdict if not, so the banner/loop stop trusting a seed that didn't land.
	if status.Reused {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		images, up, ierr := listInnerImages(ctx, session.ContainerNameForWorkspace(workspace))
		cancel()
		if up && ierr == nil {
			if appImageBeyondBase(images) != "" || len(images) > 0 {
				status.Verified = "yes"
			} else {
				status.Verified = "no"
				if status.Mode == config.DindCacheModeCopy {
					status.Reason = "Attached to " + status.CacheName + " but the inner Docker is still EMPTY — the copy seed is still running or didn't land. (A large baseline copies slowly; consider shared mode.)"
				} else {
					status.Reason = "Attached to " + status.CacheName + " (shared) but the inner Docker is empty — the cache volume may not have mounted."
				}
			}
		}
	}
	writeFilesJSON(w, status)
}

// projectRepoID resolves a project's primary repo id for the repo-scoped DinD
// baseline. Prefers the persisted RepoID (set for ALL repo-derived projects —
// PR, issue, or plain clone) and falls back to Source.RepoID for projects created
// before RepoID was recorded. Empty for a non-repo (blank/existing-dir) project.
func projectRepoID(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.RepoID != "" {
		return cfg.RepoID
	}
	if cfg.Source != nil {
		return cfg.Source.RepoID
	}
	return ""
}

// dindImage is one image present in a project's inner Docker daemon.
type dindImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	ID         string `json:"id"`
	Size       string `json:"size"`
}

// baseServiceImages are the common dependency images a project pulls (not the
// app itself). An inner docker holding ONLY these hasn't built anything worth
// saving as a baseline, so we don't nudge. Matched by repository name.
var baseServiceImages = map[string]bool{
	"postgres": true, "mysql": true, "mariadb": true, "redis": true,
	"influxdb": true, "memcached": true, "rabbitmq": true, "elasticsearch": true,
	"alpine": true, "busybox": true, "mongo": true, "nats": true, "clickhouse": true,
}

// appImageBeyondBase returns the first image that isn't a known base service
// image (and isn't <none>), or "" if the inner docker holds only base/service
// images. This is the "did the project build/load a real app image?" signal.
func appImageBeyondBase(images []dindImage) string {
	for _, img := range images {
		repo := img.Repository
		if repo == "" || repo == "<none>" {
			continue
		}
		// Strip any registry/org prefix for the base-name check (e.g. library/redis).
		base := repo
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			base = repo[i+1:]
		}
		if baseServiceImages[base] {
			continue
		}
		if img.Tag != "" && img.Tag != "<none>" {
			return repo + ":" + img.Tag
		}
		return repo
	}
	return ""
}

// listInnerImages runs `docker images` in the project's inner docker. Returns
// (images, containerUp, err). Empty list when the container is down.
func listInnerImages(ctx context.Context, container string) ([]dindImage, bool, error) {
	if !session.DockerContainerRunning(container) {
		return nil, false, nil
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", container,
		"docker", "images", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, true, err
	}
	var images []dindImage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			Repository, Tag, ID, Size string
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		images = append(images, dindImage{Repository: raw.Repository, Tag: raw.Tag, ID: raw.ID, Size: raw.Size})
	}
	return images, true, nil
}

// maybeAutoSaveRepoBaseline captures a project's inner-docker state as its repo
// baseline (repo-<repoId>) automatically — but ONLY on a safe, valuable signal:
//
//   - the project is repo-derived (we know which baseline),
//   - DinD is on,
//   - NO repo baseline exists yet (we NEVER overwrite an existing one automatically
//     — updating the shared baseline stays a deliberate Config-tab action),
//   - the inner docker holds a real app image beyond base service images (so we
//     don't bake an empty/services-only volume), verified while the container is
//     still up.
//
// It's called from the stop path BEFORE the container is removed (to read the
// inner images) but snapshots the per-workspace VOLUME, which survives the
// container — so the actual copy can run after stop. Returns the cache name on a
// save, "" otherwise. Best-effort: never blocks or fails the stop.
//
// Detection runs pre-stop; the snapshot itself is kicked off by the caller after
// the container is removed (see handleStopProject) so it doesn't delay the stop.
func (d *dashboardServer) repoBaselineAutoSaveTarget(ctx context.Context, workspace string) (repoCache, srcVol string) {
	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		return "", ""
	}
	repoID := projectRepoID(cfg) // any origin: PR, issue, or plain clone
	if !cfg.DindEnabled || repoID == "" {
		return "", ""
	}
	name := dindcache.RepoCacheName(repoID)
	if dindcache.Exists(name) {
		return "", "" // never auto-overwrite an existing baseline
	}
	images, up, ierr := listInnerImages(ctx, session.ContainerNameForWorkspace(workspace))
	if !up || ierr != nil || appImageBeyondBase(images) == "" {
		return "", "" // nothing worth saving (no app image built)
	}
	return name, config.DindVolumeName(workspace)
}

// handleDindImages lists the images present in a project's INNER docker daemon —
// what's actually cached/available inside the sandbox (so you can confirm e.g.
// the app image is there without a rebuild). This is the "live" call: it runs
// `docker exec <container> docker images` and needs the container to be up. It's
// deliberately separate from the cheap status banner.
//
//	GET /p/<id>/dind/images -> { containerUp, images: [dindImage] }
func (d *dashboardServer) handleDindImages(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	images, up, err := listInnerImages(ctx, session.ContainerNameForWorkspace(workspace))
	if !up {
		writeFilesJSON(w, map[string]any{"containerUp": false, "images": []dindImage{}})
		return
	}
	if err != nil {
		writeFilesJSON(w, map[string]any{"containerUp": true, "images": []dindImage{}, "error": "inner docker images failed: " + err.Error()})
		return
	}
	writeFilesJSON(w, map[string]any{"containerUp": true, "images": images})
}
