package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/repos"
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
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
