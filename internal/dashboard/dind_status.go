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
	writeFilesJSON(w, status)
}

// dindImage is one image present in a project's inner Docker daemon.
type dindImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	ID         string `json:"id"`
	Size       string `json:"size"`
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
	container := session.ContainerNameForWorkspace(workspace)
	if !session.DockerContainerRunning(container) {
		writeFilesJSON(w, map[string]any{"containerUp": false, "images": []dindImage{}})
		return
	}
	// Ask the INNER daemon (DOCKER_HOST is exported inside the container) for its
	// images as JSON lines. Bounded so a wedged inner daemon can't hang the request.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", container,
		"docker", "images", "--format", "{{json .}}").Output()
	if err != nil {
		writeFilesJSON(w, map[string]any{"containerUp": true, "images": []dindImage{}, "error": "inner docker images failed: " + err.Error()})
		return
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
	writeFilesJSON(w, map[string]any{"containerUp": true, "images": images})
}
