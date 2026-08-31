package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/applog"
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
	case rest == "live-origin":
		d.handleLiveOrigin(w, r)
	case rest == "actions:test":
		d.handleActionTest(w, r)
	case rest == "actions":
		d.handleActions(w, r)
	case strings.HasPrefix(rest, "actions/"):
		d.handleActionItem(w, r, strings.TrimPrefix(rest, "actions/"))
	case rest == "hooks":
		d.handleHooks(w, r)
	case strings.HasPrefix(rest, "hooks/"):
		d.handleHookItem(w, r, strings.TrimPrefix(rest, "hooks/"))
	case rest == "flows":
		d.handleFlows(w, r)
	case strings.HasPrefix(rest, "flows/"):
		d.handleFlowItem(w, r, strings.TrimPrefix(rest, "flows/"))
	case rest == "runs":
		d.handleRuns(w, r)
	case strings.HasPrefix(rest, "runs/"):
		d.handleRunItem(w, r, strings.TrimPrefix(rest, "runs/"))
	case rest == "logs":
		d.handleLogs(w, r, "")
	case strings.HasPrefix(rest, "logs/"):
		d.handleLogs(w, r, strings.TrimPrefix(rest, "logs/"))
	case rest == "triggers":
		d.handleTriggers(w, r)
	case rest == "prompts/project-start":
		d.handleProjectStartPrompt(w, r)
	case rest == "prompts/default":
		d.handleDefaultPrompt(w, r)
	case rest == "prompts/draft":
		d.handlePromptDraftWS(w, r)
	case rest == "prompts/library":
		d.handlePromptLibrary(w, r, "")
	case strings.HasPrefix(rest, "prompts/library/"):
		d.handlePromptLibrary(w, r, strings.TrimPrefix(rest, "prompts/library/"))
	case rest == "scripts/draft":
		d.handleScriptDraftWS(w, r)
	case rest == "scripts/env":
		d.handleScriptEnv(w, r)
	case rest == "prompts":
		d.handlePrompts(w, r, "")
	case strings.HasPrefix(rest, "prompts/"):
		// Generic prompt-catalog items (/api/prompts/<key>). The specific
		// project-start/default/draft cases above are matched first.
		d.handlePrompts(w, r, keyFromPromptsRest(rest))
	case rest == "openapi.json":
		d.handleOpenAPI(w, r)
	case rest == "tools":
		d.handleTools(w, r, "")
	case strings.HasPrefix(rest, "tools/"):
		d.handleTools(w, r, strings.TrimPrefix(rest, "tools/"))
	case rest == "mcp":
		d.handleMCP(w, r, "")
	case strings.HasPrefix(rest, "mcp/"):
		d.handleMCP(w, r, strings.TrimPrefix(rest, "mcp/"))
	case rest == "chat/capability":
		d.handleChatCapability(w, r)
	case rest == "skills":
		d.handleRepoSkills(w, r, "")
	case strings.HasPrefix(rest, "skills/") && strings.HasSuffix(rest, "/promote"):
		d.handleSkillPromote(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "skills/"), "/promote"))
	case strings.HasPrefix(rest, "skills/"):
		d.handleRepoSkills(w, r, strings.TrimPrefix(rest, "skills/"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/agent-context"):
		d.handleRepoAgentContext(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/agent-context"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/skills/effective"):
		d.handleRepoEffectiveSkills(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/skills/effective"))
	case strings.HasPrefix(rest, "repos/") && strings.Contains(rest, "/skills/") && strings.HasSuffix(rest, "/enabled"):
		// /api/repos/<repoID>/skills/<name>/enabled — per-repo enable/disable of a global skill.
		inner := strings.TrimPrefix(rest, "repos/")
		repoID, after, _ := strings.Cut(inner, "/skills/")
		name := strings.TrimSuffix(after, "/enabled")
		d.handleRepoSkillEnabled(w, r, repoID, name)
	case rest == "dind/caches":
		d.handleDindCaches(w, r, "")
	case strings.HasPrefix(rest, "dind/caches/"):
		d.handleDindCaches(w, r, strings.TrimPrefix(rest, "dind/caches/"))
	case rest == "prs/prune":
		d.handlePRPrune(w, r)
	case rest == "prs/inbox" && r.Method == http.MethodGet:
		// Open PRs across every repo (with repo id + GitHub number). To act on one,
		// fetch it first (POST /api/repos/<id>/prs/fetch) to get its internal id.
		d.handlePRInbox(w, r)
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/prs") && r.Method == http.MethodGet:
		d.handleRepoPRs(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/prs"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/prs/open") && r.Method == http.MethodGet:
		d.handleRepoOpenPRs(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/prs/open"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/prs/fetch") && r.Method == http.MethodPost:
		d.handleRepoPRFetch(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/prs/fetch"))
	case rest == "conductor/workers":
		d.handleConductorWorkerCreate(w, r)
	case rest == "conversations":
		d.handleConversations(w, r, "")
	case strings.HasPrefix(rest, "conversations/"):
		d.handleConversations(w, r, strings.TrimPrefix(rest, "conversations/"))
	case strings.HasPrefix(rest, "prs/"):
		// PR-scoped API routes: /api/prs/<id>/notes[/<noteId>], /api/prs/<id>/merge.
		d.handleAPIPRItem(w, r, strings.TrimPrefix(rest, "prs/"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/merge-strategy"):
		d.handleAPIRepoMergeStrategy(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/merge-strategy"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/analyze") && r.Method == http.MethodPost:
		d.handleRepoAnalyze(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/analyze"))
	case strings.HasPrefix(rest, "repos/") && strings.HasSuffix(rest, "/analysis-status") && r.Method == http.MethodGet:
		d.handleRepoAnalysisStatus(w, r, strings.TrimSuffix(strings.TrimPrefix(rest, "repos/"), "/analysis-status"))
	default:
		routeNotFound(w, r)
	}
}

// handleAPIPRItem dispatches PR-scoped /api routes. rest is the path after
// "prs/" (e.g. "42/notes", "42/notes/7", "42/merge"). These reuse the same
// handlers as the browser PR routes; the API-writes gate (applied in handleRoot)
// already fences the mutating ones.
func (d *dashboardServer) handleAPIPRItem(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	prID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		routeNotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "notes" && r.Method == http.MethodGet:
		d.handlePRNotesGet(w, r, prID)
	case action == "notes" && r.Method == http.MethodPost:
		d.handlePRNoteAdd(w, r, prID)
	case strings.HasPrefix(action, "notes/") && r.Method == http.MethodDelete:
		d.handlePRNoteRemove(w, r, strings.TrimPrefix(action, "notes/"))
	case action == "merge" && r.Method == http.MethodPost:
		d.handleAPIPRMerge(w, r, prID)
	case action == "enrich" && r.Method == http.MethodPost:
		d.handleAPIPREnrich(w, r, prID)
	case action == "analyze" && r.Method == http.MethodPost:
		d.handleAPIPRRiskStart(w, r, prID)
	case action == "analysis" && r.Method == http.MethodGet:
		d.handleAPIPRAnalysisStatus(w, r, prID)
	case action == "blocks" && r.Method == http.MethodGet:
		d.handlePRBlocks(w, r, prID)
	case action == "risk" && r.Method == http.MethodGet:
		d.handlePRRiskGet(w, r, prID)
	case action == "links" && r.Method == http.MethodGet:
		d.handlePRLinksGet(w, r, prID)
	case action == "links" && r.Method == http.MethodPost:
		d.handlePRLinkAdd(w, r, prID)
	case action == "links/suggest" && r.Method == http.MethodGet:
		d.handlePRLinkSuggest(w, r, prID)
	case action == "stack" && r.Method == http.MethodGet:
		d.handlePRStack(w, r, prID)
	case strings.HasPrefix(action, "links/") && r.Method == http.MethodDelete:
		d.handlePRLinkRemove(w, r, strings.TrimPrefix(action, "links/"))
	default:
		routeNotFound(w, r)
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

// handleTriggers: GET /api/triggers — the user-facing trigger catalog (friendly
// labels + the built-in step for each), the single source of truth for the
// redesigned Automations cards. Static; no store access needed.
func (d *dashboardServer) handleTriggers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"triggers": automations.Triggers()})
}

// handleDefaultPrompt: GET /api/prompts/default — returns the editable global
// project-start prompt action, creating it (seeded with the built-in default)
// if it doesn't exist. This gives the Prompts section a concrete action to edit
// (PUT /api/actions/<id>) rather than a phantom default.
func (d *dashboardServer) handleDefaultPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	a, err := svc.EnsureProjectStartPrompt()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var spec automations.PromptSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	writeJSON(w, map[string]any{"id": a.ID, "name": a.Name, "template": spec.Template})
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
	// "<id>/secrets" → view (masked) / set this script's injected secrets.
	if idStr, ok := strings.CutSuffix(rest, "/secrets"); ok {
		d.handleActionSecrets(w, r, svc, idStr)
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
	runner := d.automationsRunner(svc)
	res, err := runner.RunAction(r.Context(), id, automations.TriggerAPI, rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.applog().InfoCtx(r.Context(), applog.Entry{
		Level: levelForStatus(res.Status), Category: applog.CatAutomation, Event: "automation.run",
		Message: applog.Fmt("Ran action %q — %s", res.Name, res.Status),
		Status:  res.Status, RepoID: rc.RepoID, Meta: map[string]any{"action": res.Name, "kind": res.Kind},
	})
	writeJSON(w, res)
}

// levelForStatus maps a step/run status to a log level.
func levelForStatus(status string) string {
	if status == automations.StatusError {
		return applog.LevelError
	}
	return applog.LevelInfo
}

// handleActionTest runs an UNSAVED action ad-hoc for the "test this step" flow:
// POST /api/actions:test {kind, spec, context?}. It executes the in-memory
// action via the registry and returns the StepResult WITHOUT persisting the
// action or recording a run — so a user can try a bash script (or webhook, etc.)
// while composing it, before saving.
func (d *dashboardServer) handleActionTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	var body struct {
		Kind    string                 `json:"kind"`
		Spec    string                 `json:"spec"`
		Context automations.RunContext `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Kind == "" {
		http.Error(w, "kind and spec are required", http.StatusBadRequest)
		return
	}
	runner := d.automationsRunner(svc)
	res := runner.RunEphemeral(r.Context(), automations.Action{Kind: body.Kind, Spec: body.Spec, Name: "test"}, body.Context)
	d.applog().InfoCtx(r.Context(), applog.Entry{
		Level: levelForStatus(res.Status), Category: applog.CatScript, Event: "script.test",
		Message: applog.Fmt("Test-ran a %s step — %s", body.Kind, res.Status),
		Status:  res.Status, DurationMs: res.Duration, Meta: map[string]any{"kind": body.Kind},
	})
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
