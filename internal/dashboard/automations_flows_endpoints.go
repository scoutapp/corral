package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
)

// /api/flows — composed units of work.
//
//	GET    /api/flows            list (?repo= includes repo's + global)
//	POST   /api/flows            create {name, scope?, repoId?, steps?[]}
//	GET    /api/flows/<id>       fetch one (with steps)
//	POST   /api/flows/<id>/steps append a step {actionId, position?, stepKey?, dependsOn?[]}
//	DELETE /api/flows/<id>       delete
//	POST   /api/flows/<id>:run   run ad-hoc (body = run context)

func (d *dashboardServer) handleFlows(w http.ResponseWriter, r *http.Request) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		flows, err := svc.ListFlows(r.URL.Query().Get("repo"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"flows": flows})
	case http.MethodPost:
		var body struct {
			Name   string                   `json:"name"`
			Scope  string                   `json:"scope"`
			RepoID string                   `json:"repoId"`
			Steps  []automations.FlowStep   `json:"steps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		flow, err := svc.CreateFlow(automations.Flow{Name: body.Name, Scope: body.Scope, RepoID: body.RepoID})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Optional inline steps on create.
		for i, st := range body.Steps {
			st.FlowID = flow.ID
			if st.Position == 0 {
				st.Position = i
			}
			if _, err := svc.AddStep(st); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		full, _ := svc.Flow(flow.ID)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, full)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *dashboardServer) handleFlowItem(w http.ResponseWriter, r *http.Request, rest string) {
	svc := d.automationsService(w)
	if svc == nil {
		return
	}

	// "<id>:run"
	if idStr, ok := strings.CutSuffix(rest, ":run"); ok {
		d.handleFlowRun(w, r, svc, idStr)
		return
	}
	// "<id>/steps"
	if idStr, ok := strings.CutSuffix(rest, "/steps"); ok {
		d.handleFlowAddStep(w, r, svc, idStr)
		return
	}

	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		http.Error(w, "bad flow id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		flow, err := svc.Flow(id)
		if err != nil {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		writeJSON(w, flow)
	case http.MethodDelete:
		if err := svc.DeleteFlow(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *dashboardServer) handleFlowAddStep(w http.ResponseWriter, r *http.Request, svc *automations.Service, idStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad flow id", http.StatusBadRequest)
		return
	}
	var st automations.FlowStep
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil || st.ActionID == 0 {
		http.Error(w, "actionId is required", http.StatusBadRequest)
		return
	}
	st.FlowID = id
	added, err := svc.AddStep(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, added)
}

func (d *dashboardServer) handleFlowRun(w http.ResponseWriter, r *http.Request, svc *automations.Service, idStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad flow id", http.StatusBadRequest)
		return
	}
	var rc automations.RunContext
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&rc)
	}
	runner := automations.NewRunner(svc, automationsRegistry())
	res, err := runner.RunFlow(r.Context(), id, automations.TriggerAPI, rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}
