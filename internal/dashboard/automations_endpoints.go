package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
)

// Automations REST surface, mounted under /api/. This is the API-first control
// plane: the dashboard UI is one client; a future corral CLI + OpenAPI-driven
// macros are others. Every operation here is a plain HTTP endpoint so those
// later callers reuse it verbatim.
//
//	GET    /api/actions            list (?repo=<id> includes that repo's + global)
//	POST   /api/actions            create
//	GET    /api/actions/<id>       fetch one
//	PUT    /api/actions/<id>       update (name + spec)
//	DELETE /api/actions/<id>       delete
//	POST   /api/actions/<id>:run   run ad-hoc (body = run context) — what macros call
//	GET    /api/hooks              list (?event=&repo=)
//	POST   /api/hooks              bind an event → action|flow
//	DELETE /api/hooks/<id>         unbind
//	GET    /api/runs               recent run history (?limit=)
//	GET    /api/runs/<id>          one run

// automationsService opens the shared store and returns the automations service,
// or writes a 500 and returns nil.
func (d *dashboardServer) automationsService(w http.ResponseWriter) *automations.Service {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return nil
	}
	return automations.New(s)
}

// handleAPI dispatches everything under /api/. rest is the path after "/api/".
func (d *dashboardServer) handleAPI(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "actions":
		d.handleActions(w, r)
	case strings.HasPrefix(rest, "actions/"):
		d.handleActionItem(w, r, strings.TrimPrefix(rest, "actions/"))
	case rest == "hooks":
		d.handleHooks(w, r)
	case strings.HasPrefix(rest, "hooks/"):
		d.handleHookItem(w, r, strings.TrimPrefix(rest, "hooks/"))
	case rest == "runs":
		d.handleRuns(w, r)
	case strings.HasPrefix(rest, "runs/"):
		d.handleRunItem(w, r, strings.TrimPrefix(rest, "runs/"))
	case rest == "prompts/project-start":
		d.handleProjectStartPrompt(w, r)
	case rest == "prompts/draft":
		d.handlePromptDraftWS(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleProjectStartPrompt: GET /api/prompts/project-start?repo=<id> — returns
// the effective project-start prompt template for a repo (with its source:
// repo / global / default) plus the prompt-template actions available to pick
// from. This backs the [Verify in sandbox ▾] split-button picker.
func (d *dashboardServer) handleProjectStartPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	repoID := r.URL.Query().Get("repo")

	tmpl, source, err := svc.ResolveProjectStartPrompt(repoID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Prompt-template presets available to this repo (its own + global).
	all, err := svc.ListActions(repoID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type preset struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Scope    string `json:"scope"`
		Template string `json:"template"`
	}
	presets := []preset{}
	for _, a := range all {
		if a.Kind != automations.KindClaudePrompt {
			continue
		}
		var spec automations.PromptSpec
		_ = json.Unmarshal([]byte(a.Spec), &spec)
		presets = append(presets, preset{ID: a.ID, Name: a.Name, Scope: a.Scope, Template: spec.Template})
	}

	writeJSON(w, map[string]any{
		"template": tmpl,
		"source":   source, // repo | global | default
		"presets":  presets,
	})
}

// --- /api/actions ----------------------------------------------------------

func (d *dashboardServer) handleActions(w http.ResponseWriter, r *http.Request) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		acts, err := svc.ListActions(r.URL.Query().Get("repo"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"actions": acts})
	case http.MethodPost:
		var a automations.Action
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if a.Name == "" || a.Kind == "" {
			http.Error(w, "name and kind are required", http.StatusBadRequest)
			return
		}
		created, err := svc.CreateAction(a)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleActionItem handles /api/actions/<id> and /api/actions/<id>:run. The
// ":run" suffix (a sub-resource verb, REST-verb style) triggers an ad-hoc run.
func (d *dashboardServer) handleActionItem(w http.ResponseWriter, r *http.Request, rest string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}

	// "<id>:run" → run the action ad-hoc.
	if idStr, ok := strings.CutSuffix(rest, ":run"); ok {
		d.handleActionRun(w, r, svc, idStr)
		return
	}

	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		http.Error(w, "bad action id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a, err := svc.Action(id)
		if err != nil {
			http.Error(w, "action not found", http.StatusNotFound)
			return
		}
		writeJSON(w, a)
	case http.MethodPut:
		var body struct {
			Name string `json:"name"`
			Spec string `json:"spec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		upd, err := svc.UpdateAction(id, body.Name, body.Spec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, upd)
	case http.MethodDelete:
		if err := svc.DeleteAction(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleActionRun executes an action ad-hoc (POST /api/actions/<id>:run). The
// request body is the run context (event/repo/vars); this is the entry point a
// macro or the CLI uses to trigger a unit of work directly.
func (d *dashboardServer) handleActionRun(w http.ResponseWriter, r *http.Request, svc *automations.Service, idStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad action id", http.StatusBadRequest)
		return
	}
	var rc automations.RunContext
	if r.Body != nil {
		// An empty body is fine (manual run with no context).
		_ = json.NewDecoder(r.Body).Decode(&rc)
	}
	runner := automations.NewRunner(svc, automations.DefaultRegistry())
	res, err := runner.RunAction(r.Context(), id, automations.TriggerAPI, rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}

// --- /api/hooks ------------------------------------------------------------

func (d *dashboardServer) handleHooks(w http.ResponseWriter, r *http.Request) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		event := r.URL.Query().Get("event")
		if event == "" {
			http.Error(w, "event query param is required", http.StatusBadRequest)
			return
		}
		hooks, err := svc.HooksForEvent(event, r.URL.Query().Get("repo"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"hooks": hooks})
	case http.MethodPost:
		var h automations.Hook
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if h.Event == "" || h.TargetKind == "" || h.TargetID == 0 {
			http.Error(w, "event, targetKind and targetId are required", http.StatusBadRequest)
			return
		}
		created, err := svc.CreateHook(h)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *dashboardServer) handleHookItem(w http.ResponseWriter, r *http.Request, idStr string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad hook id", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := svc.DeleteHook(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// --- /api/runs -------------------------------------------------------------

func (d *dashboardServer) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := svc.RecentRuns(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"runs": runs})
}

func (d *dashboardServer) handleRunItem(w http.ResponseWriter, r *http.Request, idStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	run, err := svc.Run(id)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, run)
}
