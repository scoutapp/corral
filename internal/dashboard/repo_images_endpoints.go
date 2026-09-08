package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/scoutapp/corral/internal/dindcache"
	"github.com/scoutapp/corral/internal/repos"
	"github.com/scoutapp/corral/internal/session"
)

// GET /api/repos/<id>/images — a repo-level view of the Docker images Corral has
// for this repo:
//   - baseline: the repo's DinD baseline cache (repo-<id>) if one exists — the
//     snapshot new projects from this repo reuse — with its on-disk size. This is
//     the durable, host-side "built images for this repo" answer (present even
//     when nothing is running).
//   - liveImages: if a project cloned from this repo is currently RUNNING, the
//     images in its inner Docker daemon (a live `docker images`). Empty when no
//     such project is up (the baseline is then the only signal).
//
// Reads only; the sandbox never reaches here. Building an image on demand is a
// separate action (create a project, build inside it, then POST /api/dind/caches
// to snapshot it as the baseline — see the corral-api skill).
func (d *dashboardServer) handleRepoImages(w http.ResponseWriter, r *http.Request, repoID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repo, err := repos.Get(repoID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	resp := map[string]any{
		"repoId": repo.ID,
		"name":   repo.Name,
	}

	// The repo's baseline cache, if it's been built/saved.
	baselineName := dindcache.RepoCacheName(repo.ID)
	if caches, err := dindcache.List(); err == nil {
		for _, c := range caches {
			if c.Name == baselineName {
				resp["baseline"] = map[string]any{"name": c.Name, "bytes": c.Bytes}
				break
			}
		}
	}
	if _, ok := resp["baseline"]; !ok {
		resp["baseline"] = nil
	}

	// Live images from a running project cloned from this repo, if any.
	resp["liveImages"] = []dindImage{}
	resp["liveProject"] = nil
	if ws := d.runningWorkspaceForRepo(repo.ID); ws != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		imgs, up, ierr := listInnerImages(ctx, session.ContainerNameForWorkspace(ws))
		if up {
			resp["liveProject"] = ProjectID(ws)
			if ierr == nil && imgs != nil {
				resp["liveImages"] = imgs
			}
		}
	}

	writeJSON(w, resp)
}

// runningWorkspaceForRepo returns the workspace of a RUNNING project cloned from
// repoID (the first found), or "" if none is up. Used to surface live inner-docker
// images for the repo without requiring the caller to know a project id.
func (d *dashboardServer) runningWorkspaceForRepo(repoID string) string {
	reg, err := readRegistry()
	if err != nil {
		return ""
	}
	for _, p := range reg.Projects {
		cfg, err := readConfigForWorkspace(p.Workspace)
		if err != nil || projectRepoID(cfg) != repoID {
			continue
		}
		if session.DockerContainerRunning(session.ContainerNameForWorkspace(p.Workspace)) {
			return p.Workspace
		}
	}
	return ""
}
