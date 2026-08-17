package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// The named-prompt library — reusable prompts a user saves by name and picks at
// project/issue start, alongside the fixed catalog. Backed by claude_prompt
// actions (see automations/prompts_library.go); this is just the REST surface.
//
//	GET    /api/prompts/library[?repo=<id>]  list saved prompts (repo's + global)
//	POST   /api/prompts/library              create {name, template, description?, repo?}
//	PUT    /api/prompts/library/<id>         update {name?, template, description?}
//	DELETE /api/prompts/library/<id>         delete
//
// Mutating calls are gated for the CLI/Claude by the API-writes gate (never the
// browser), like every other /api/ write.
func (d *dashboardServer) handlePromptLibrary(w http.ResponseWriter, r *http.Request, idStr string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}

	// Item routes: /api/prompts/library/<id>
	if idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "bad prompt id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var body struct {
				Name        string `json:"name"`
				Template    string `json:"template"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			np, err := svc.UpdateNamedPrompt(id, body.Name, body.Template, body.Description)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, np)
		case http.MethodDelete:
			if err := svc.DeleteNamedPrompt(id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Collection routes: /api/prompts/library
	switch r.Method {
	case http.MethodGet:
		prompts, err := svc.ListNamedPrompts(r.URL.Query().Get("repo"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"prompts": prompts})
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Template    string `json:"template"`
			Description string `json:"description"`
			Repo        string `json:"repo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		np, err := svc.CreateNamedPrompt(body.Name, body.Template, body.Description, body.Repo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, np)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
