package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
)

// /api/prompts — the editable prompt catalog, backing the Prompts carousel.
//
//	GET    /api/prompts[?repo=<id>]   catalog with each prompt's default,
//	                                  effective text, source, slots, usedWhen,
//	                                  and whether it's overridden at this scope.
//	PUT    /api/prompts/<key>[?repo=] save an override (body {template})
//	DELETE /api/prompts/<key>[?repo=] reset (remove the override at this scope)
//
// repo scope: with ?repo=<id> the effective text resolves repo→global→default
// and edits write a repo-scoped override; without it, global→default and edits
// write the global override.

func (d *dashboardServer) handlePrompts(w http.ResponseWriter, r *http.Request, rest string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	repoID := r.URL.Query().Get("repo")

	// Item routes: /api/prompts/<key>
	if rest != "" {
		key := rest
		switch r.Method {
		case http.MethodPut:
			var body struct {
				Template string `json:"template"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if _, ok := automations.PromptDefFor(key); !ok {
				http.Error(w, "unknown prompt key", http.StatusNotFound)
				return
			}
			if _, err := svc.SetPromptOverride(key, repoID, body.Template); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			d.writePromptItem(w, svc, key, repoID)
		case http.MethodDelete:
			if err := svc.ClearPromptOverride(key, repoID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			d.writePromptItem(w, svc, key, repoID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Collection: GET /api/prompts
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := []map[string]any{}
	for _, def := range automations.PromptCatalog() {
		eff, source := svc.ResolvePrompt(def.Key, repoID)
		out = append(out, promptItem(def, eff, source, repoID))
	}
	writeJSON(w, map[string]any{"prompts": out})
}

// writePromptItem re-reads and returns one prompt's current state after a
// mutation, so the UI updates in place.
func (d *dashboardServer) writePromptItem(w http.ResponseWriter, svc *automations.Service, key, repoID string) {
	def, _ := automations.PromptDefFor(key)
	eff, source := svc.ResolvePrompt(key, repoID)
	writeJSON(w, promptItem(def, eff, source, repoID))
}

// promptItem shapes one catalog entry for the API. overridden is true when the
// effective text comes from an override at the CURRENT scope (repo when scoped,
// else global) — i.e. Reset would change something here.
func promptItem(def automations.PromptDef, effective, source, repoID string) map[string]any {
	overridden := (repoID != "" && source == "repo") || (repoID == "" && source == "global")
	return map[string]any{
		"key":        def.Key,
		"name":       def.Name,
		"usedWhen":   def.UsedWhen,
		"slots":      def.Slots,
		"default":    def.Default,
		"effective":  effective,
		"source":     source, // repo | global | default
		"overridden": overridden,
	}
}

// keyFromPromptsRest extracts the <key> from "prompts/<key>" style rest paths.
func keyFromPromptsRest(rest string) string {
	return strings.TrimPrefix(rest, "prompts/")
}
