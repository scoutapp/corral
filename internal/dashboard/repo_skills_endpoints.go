package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Per-repo skills + agent context (#5). Storage is repo-scoped auto_actions
// (automations/repo_skills.go); this is the REST surface. The dashboard injects
// these into a sandbox checkout at project-create.
//
//	GET    /api/skills?repo=<id>       list a repo's skills
//	POST   /api/skills                 create {repo, name, content}
//	PUT    /api/skills/<id>            update {name?, content}
//	DELETE /api/skills/<id>            delete
//
//	GET    /api/repos/<id>/agent-context → { content }
//	PUT    /api/repos/<id>/agent-context   { content }   (empty clears)
func (d *dashboardServer) handleRepoSkills(w http.ResponseWriter, r *http.Request, idStr string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}

	if idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "bad skill id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var body struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			sk, err := svc.UpdateRepoSkill(id, body.Name, body.Content)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, sk)
		case http.MethodDelete:
			if err := svc.DeleteRepoSkill(id); err != nil {
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
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			http.Error(w, "repo is required", http.StatusBadRequest)
			return
		}
		skills, err := svc.ListRepoSkills(repo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"skills": skills})
	case http.MethodPost:
		var body struct {
			Repo    string `json:"repo"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		sk, err := svc.CreateRepoSkill(body.Repo, body.Name, body.Content)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, sk)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *dashboardServer) handleRepoAgentContext(w http.ResponseWriter, r *http.Request, repoID string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	if repoID == "" {
		http.Error(w, "repo id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		content, err := svc.RepoAgentContext(repoID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"content": content})
	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := svc.SetRepoAgentContext(repoID, body.Content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
