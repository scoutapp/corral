package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
)

// handleRepos serves the repos list:
//
//	GET  /repos       — list repos
//	POST /repos       — add a repo {name?, url?, localPath?, isPrivate?} (clones the cache mirror)
func (d *dashboardServer) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := repos.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFilesJSON(w, map[string]any{"repos": list})
	case http.MethodPost:
		var body struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			LocalPath string `json:"localPath"`
			IsPrivate bool   `json:"isPrivate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.URL) == "" && strings.TrimSpace(body.LocalPath) == "" {
			http.Error(w, "a url or localPath is required", http.StatusBadRequest)
			return
		}
		// Add clones the mirror, which can take a while / hit the network. Kept
		// synchronous for now (the UI shows a spinner); a progress stream is a
		// future enhancement.
		repo, err := repos.Add(repos.AddOptions{
			Name: body.Name, URL: body.URL, LocalPath: body.LocalPath, IsPrivate: body.IsPrivate,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeFilesJSON(w, map[string]any{"repo": repo})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRepoItem serves per-repo actions parsed from the path after "/repos/":
//
//	POST   /repos/<id>/fetch — refresh the cache mirror
//	DELETE /repos/<id>       — remove the repo + its cache
func (d *dashboardServer) handleRepoItem(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "fetch" && r.Method == http.MethodPost:
		if err := repos.Fetch(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		repo, _ := repos.Get(id)
		writeFilesJSON(w, map[string]any{"repo": repo})
	case action == "" && r.Method == http.MethodDelete:
		if err := repos.Remove(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeFilesJSON(w, map[string]any{"ok": true})
	// PR Review (see internal/prreview).
	case action == "forensics" && r.Method == http.MethodGet:
		d.handleRepoForensics(w, r, id)
	case action == "analyze" && r.Method == http.MethodPost:
		d.handleRepoAnalyze(w, r, id)
	case action == "prs" && r.Method == http.MethodGet:
		d.handleRepoPRs(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// prreviewService opens the shared store and returns a PR Review service, or
// writes a 500 and returns nil if the DB can't be opened.
func (d *dashboardServer) prreviewService(w http.ResponseWriter, id string) *prreview.Service {
	if _, err := repos.Get(id); err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return nil
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return nil
	}
	return prreview.New(s)
}

// handleRepoForensics: GET /repos/<id>/forensics — per-file hot list.
func (d *dashboardServer) handleRepoForensics(w http.ResponseWriter, r *http.Request, id string) {
	svc := d.prreviewService(w, id)
	if svc == nil {
		return
	}
	stats, err := svc.Forensics(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"files": stats})
}

// handleRepoAnalyze: POST /repos/<id>/analyze — run git forensics over the
// repo's cache mirror and (re)populate pr_file_stats. Synchronous for now (the
// UI shows a spinner), mirroring how repo Add/Fetch are handled; a progress
// stream is a future enhancement.
func (d *dashboardServer) handleRepoAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	if repo.CachePath == "" {
		http.Error(w, "repo has no local cache to analyze", http.StatusBadRequest)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := prreview.New(s).Analyze(id, repo.CachePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"files": stats})
}

// handleRepoPRs: GET /repos/<id>/prs — fetched pull requests for the repo.
func (d *dashboardServer) handleRepoPRs(w http.ResponseWriter, r *http.Request, id string) {
	svc := d.prreviewService(w, id)
	if svc == nil {
		return
	}
	prs, err := svc.PRs(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"prs": prs})
}
