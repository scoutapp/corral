package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/dindcache"
	"github.com/scoutapp/corral/internal/repos"
)

// POST /api/repos/<id>/build-image { command?: "<build cmd>", imageTag?: "<tag>" }
// kicks a detached background job that builds the repo's Docker image and saves it
// as the repo's DinD baseline cache (repo-<id>), so future projects from the repo
// reuse it. The job creates a THROWAWAY sandbox project on the repo, builds inside
// its inner Docker, snapshots the volume into the baseline, then tears the project
// down. Returns { jobId } immediately; watch it in the Global chat's Jobs list.
//
// The build command is INFERRED (a Dockerfile / docker-compose.yml / the repo's
// documented build) unless `command` overrides it. Reuses the worker-job harness:
// a headless build worker drives the existing create/start/exec/snapshot/remove
// API — no bespoke lifecycle code to keep in sync.
func (d *dashboardServer) handleRepoBuildImage(w http.ResponseWriter, r *http.Request, repoID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repo, err := repos.Get(repoID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Command  string `json:"command"`  // optional explicit build command
		ImageTag string `json:"imageTag"` // optional resulting image tag/name
	}
	// Body is optional; ignore a decode error on an empty/garbage body.
	_ = json.NewDecoder(r.Body).Decode(&body)

	prompt := buildImagePrompt(repo, strings.TrimSpace(body.Command), strings.TrimSpace(body.ImageTag))
	title := "Build image · " + repo.Name

	// captureKind "build-image" tags the job's conversation; parent-links to the
	// caller's conversation when a captured Claude drove this via corral api.
	job, err := d.startWorkerJob(prompt, title, parentConvFromRequest(r), "build-image", repo.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"jobId": job.ID, "title": job.Title})
}

// buildImagePrompt renders the deterministic build recipe the background worker
// follows: create a throwaway project on the repo, build the image in its inner
// Docker, snapshot it as the baseline, then remove the project. All context the
// worker needs is inline (it starts fresh in a neutral dir).
func buildImagePrompt(repo *repos.Repo, command, imageTag string) string {
	branch := repo.DefaultBranch
	if branch == "" {
		branch = "the default branch"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Build the Docker image for the repo %q (id `%s`, %s) and save it as this repo's DinD "+
		"baseline cache so future projects reuse it. Work entirely through `corral api`/`corral` — do NOT build "+
		"on this host directly.\n\n", repo.Name, repo.ID, branch)

	b.WriteString("STEPS:\n")
	b.WriteString("1. Check what's already cached: `corral api GET /api/repos/" + repo.ID + "/images`. " +
		"If a `baseline` already exists and the caller didn't ask to rebuild, report that and stop.\n")
	b.WriteString("2. Create a throwaway sandbox project on the repo (DinD is on by default) and start it:\n" +
		"   `corral api POST /projects/create -d '{\"repoId\":\"" + repo.ID + "\",\"prompt\":\"Idle — this project is a build host.\"}'` → capture the `id`.\n" +
		"   `corral api POST /p/<id>/start` and wait until the container is up (`corral api GET /status` shows it, or `corral dind status <id>`).\n")
	if command != "" {
		b.WriteString("3. Build the image INSIDE the sandbox with the command the caller specified:\n" +
			"   run `" + command + "` inside the project (via `corral project exec <id> -- bash -lc '<command>'`, or the sandbox's own shell). " +
			"Wait for it to finish; a build can take a while, so poll/block IN-TURN until it's done.\n")
	} else {
		b.WriteString("3. Build the image INSIDE the sandbox. Infer the build from the repo: prefer its own " +
			"`Dockerfile` (`docker build -t <tag> .`) or `docker-compose.yml` (`docker compose build`); otherwise " +
			"use the repo's documented build (README/CI). Run it via `corral project exec <id> -- bash -lc '<cmd>'`. " +
			"Wait for it to finish (block/poll in-turn). If you genuinely can't determine a build command, stop and " +
			"report what you found rather than guessing.\n")
	}
	if imageTag != "" {
		fmt.Fprintf(&b, "   Tag the resulting image `%s`.\n", imageTag)
	}
	b.WriteString("4. Confirm the image exists: `corral api GET /p/<id>/dind/images` should list it.\n")
	b.WriteString("5. Snapshot it as the repo baseline so future projects reuse it:\n" +
		"   `corral api POST /api/dind/caches -d '{\"name\":\"" + dindcache.RepoCacheName(repo.ID) + "\",\"project\":\"<id>\"}'`.\n" +
		"   (A snapshot captures IMAGES + NAMED VOLUMES, not a running container's writable layer — so make sure the " +
		"image is actually built/committed, not just present in a running container.)\n")
	b.WriteString("6. Tear down the throwaway project: `corral api DELETE /projects/<id>` (or `POST /p/<id>/stop` then remove).\n\n")
	b.WriteString("Report the final result: the image built + tag, the baseline cache name and size, and anything you couldn't do. " +
		"Remember you are a detached turn — block on the long build IN-TURN (bounded poll) rather than starting it and ending your turn.")
	return b.String()
}
