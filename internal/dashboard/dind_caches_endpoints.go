package dashboard

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
)

// DinD data-cache REST surface (#8). A cache is a reusable named docker volume
// (corral-dind-cache-<slug>) a DinD project can start FROM, so images built and
// data seeded inside the inner docker aren't rebuilt for every fresh project.
//
//	GET    /api/dind/caches            list all caches (name, volume, size)
//	POST   /api/dind/caches            snapshot a project's DinD volume into a
//	                                   cache: { name, project: <project-id> }
//	DELETE /api/dind/caches/<name>     delete a cache
//
// These operate on docker volumes on the HOST — the sandbox never reaches here.
func (d *dashboardServer) handleDindCaches(w http.ResponseWriter, r *http.Request, name string) {
	if name != "" {
		// /api/dind/caches/<name> — the name may be URL-escaped.
		decoded, err := url.PathUnescape(name)
		if err != nil {
			http.Error(w, "bad cache name", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := dindcache.Delete(decoded); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		caches, err := dindcache.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if caches == nil {
			caches = []dindcache.Cache{}
		}
		writeJSON(w, map[string]any{"caches": caches})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			Project string `json:"project"` // project id to snapshot
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Project == "" {
			http.Error(w, "project is required", http.StatusBadRequest)
			return
		}
		if !dindcache.ValidName(body.Name) {
			http.Error(w, "invalid cache name: use letters, digits, dashes, underscores (max 64)", http.StatusBadRequest)
			return
		}
		workspace, err := lookupWorkspaceByID(body.Project)
		if err != nil {
			http.Error(w, "unknown project", http.StatusNotFound)
			return
		}
		// Snapshot the project's current inner-docker data root into the cache.
		srcVol := config.DindVolumeName(workspace)
		cache, err := dindcache.CreateFromVolume(body.Name, srcVol)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, cache)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
