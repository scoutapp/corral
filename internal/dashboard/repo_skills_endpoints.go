package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/scoutapp/corral/internal/automations"
)

// Skills + agent context. Storage is auto_actions (automations/repo_skills.go);
// this is the REST surface. The dashboard injects the resolved set into a sandbox
// checkout at project-create.
//
// Skills come at two scopes — a shared GLOBAL catalog and a repo's OWN skills:
//
//	GET    /api/skills?repo=<id>       list a repo's own skills
//	GET    /api/skills?scope=global    list the global skill catalog
//	POST   /api/skills                 create {name, content, scope?, autoAll?, repo?}
//	                                     scope="global" → global skill (autoAll opt);
//	                                     else repo skill (repo required)
//	PUT    /api/skills/<id>            update {name?, content, autoAll?}
//	                                     (routes to global vs repo by the action's scope)
//	DELETE /api/skills/<id>            delete
//	POST   /api/skills/<id>/promote    {autoAll?} promote a repo skill to global
//
//	GET    /api/repos/<id>/skills/effective          resolved injected set + tri-state
//	PUT    /api/repos/<id>/skills/<name>/enabled      {enabled} force a global on/off here
//	DELETE /api/repos/<id>/skills/<name>/enabled      clear the override (inherit)
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
				AutoAll bool   `json:"autoAll"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			// Route to the right updater by the action's current scope.
			a, err := svc.Action(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			var sk automations.RepoSkill
			if a.Scope == automations.ScopeGlobal {
				sk, err = svc.UpdateGlobalSkill(id, body.Name, body.Content, body.AutoAll)
			} else {
				sk, err = svc.UpdateRepoSkill(id, body.Name, body.Content)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, sk)
		case http.MethodDelete:
			// Delete the right scope; both guard the kind.
			a, err := svc.Action(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			if a.Scope == automations.ScopeGlobal {
				err = svc.DeleteGlobalSkill(id)
			} else {
				err = svc.DeleteRepoSkill(id)
			}
			if err != nil {
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
		if r.URL.Query().Get("scope") == automations.ScopeGlobal {
			skills, err := svc.ListGlobalSkills()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"skills": skills})
			return
		}
		repo := r.URL.Query().Get("repo")
		if repo == "" {
			http.Error(w, "repo or scope=global is required", http.StatusBadRequest)
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
			Scope   string `json:"scope"`
			AutoAll bool   `json:"autoAll"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		var sk automations.RepoSkill
		var err error
		if body.Scope == automations.ScopeGlobal {
			sk, err = svc.CreateGlobalSkill(body.Name, body.Content, body.AutoAll)
		} else {
			sk, err = svc.CreateRepoSkill(body.Repo, body.Name, body.Content)
		}
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

// handleSkillPromote: POST /api/skills/<id>/promote {autoAll?} — promote a repo
// skill to the global catalog for reuse across repos.
func (d *dashboardServer) handleSkillPromote(w http.ResponseWriter, r *http.Request, idStr string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad skill id", http.StatusBadRequest)
		return
	}
	var body struct {
		AutoAll bool `json:"autoAll"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body → autoAll=false
	sk, err := svc.PromoteSkillToGlobal(id, body.AutoAll)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, sk)
}

// handleRepoEffectiveSkills: GET /api/repos/<id>/skills/effective — the resolved
// set injected for this repo, plus each global's tri-state for the settings UI
// (inherit / on / off), so the UI can render the checkboxes without recomputing.
func (d *dashboardServer) handleRepoEffectiveSkills(w http.ResponseWriter, r *http.Request, repoID string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	if repoID == "" {
		http.Error(w, "repo id required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	eff, err := svc.EffectiveSkills(repoID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"skills": eff})
}

// handleRepoSkillEnabled: PUT/DELETE /api/repos/<id>/skills/<name>/enabled —
// force a global skill on/off for this repo (PUT {enabled}) or clear the override
// so it inherits the global's auto-add default (DELETE).
func (d *dashboardServer) handleRepoSkillEnabled(w http.ResponseWriter, r *http.Request, repoID, name string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	if repoID == "" || name == "" {
		http.Error(w, "repo id and skill name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := svc.SetRepoSkillEnabled(repoID, name, body.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := svc.ClearRepoSkillPref(repoID, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
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
