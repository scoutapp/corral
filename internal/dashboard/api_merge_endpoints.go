package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
)

// handleAPIRepoMergeStrategy is the programmatic (/api) view+set of a repo's
// merge strategy. GET returns the allowed/preferred/global/effective state; PUT
// sets the PER-REPO preference (the preferred way to record a choice — repo
// beats global). The write is gated by the API-writes setting (in handleRoot).
//
//	GET /api/repos/<id>/merge-strategy
//	PUT /api/repos/<id>/merge-strategy   { "strategy": "squash"|"merge"|"rebase"|"" }
func (d *dashboardServer) handleAPIRepoMergeStrategy(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		d.handleRepoMergeStrategyGet(w, r, id)
	case http.MethodPut:
		var body struct {
			Strategy string `json:"strategy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := repos.SetPreferredMergeStrategy(id, body.Strategy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeFilesJSON(w, map[string]any{"ok": true, "preferred": body.Strategy})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIPRMerge merges a PR programmatically, matching the dashboard button's
// three modes. A strategy must be resolvable from the repo preference or the
// global default (or passed explicitly in the request); otherwise it errors,
// steering the caller to set the PER-REPO default first.
//
//		POST /api/prs/<id>/merge   { "mode": "host"|"sandbox"|"plain", "strategy?": "..." }
//
//	  - plain:   merges directly via `gh pr merge` (fails if not mergeable).
//	  - host:    starts a detached host-merge background job → { jobId } (watch it in
//	    the Work tab). NOT sandboxed.
//	  - sandbox: reports that sandbox mode is launched from the dashboard (it needs a
//	    project + interactive start that the headless API doesn't drive).
func (d *dashboardServer) handleAPIPRMerge(w http.ResponseWriter, r *http.Request, prID int64) {
	var body struct {
		Mode     string `json:"mode"`
		Strategy string `json:"strategy"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	mode := body.Mode
	if mode == "" {
		mode = config.ReadGlobalSettings().MergeModeOrDefault()
	}
	if !config.ValidMergeMode(mode) {
		http.Error(w, fmt.Sprintf("invalid mode %q (want host|sandbox|plain)", mode), http.StatusBadRequest)
		return
	}

	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	repoID, err := svc.RepoIDForPR(prID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ownerName := d.ownerNameForPR(svc, prID)
	if ownerName == "" {
		http.Error(w, "repo is not a GitHub remote", http.StatusBadRequest)
		return
	}

	// Resolve the strategy. Priority: explicit request value → repo preference →
	// global default. If NONE of those is set, error and steer the caller to set a
	// per-repo default (the preferred place to record it), or a global one.
	allowed, preferred := d.resolveRepoMergeMethods(repoID, ownerName)
	strategy := body.Strategy
	if strategy == "" {
		if preferred == "" && config.ReadGlobalSettings().MergeStrategy == "" {
			http.Error(w,
				"no merge strategy set for this repo. Set a per-repo default (preferred): "+
					"PUT /api/repos/"+repoID+"/merge-strategy {\"strategy\":\"squash|merge|rebase\"}, "+
					"or a global default in the dashboard's Global settings — or pass \"strategy\" in this request.",
				http.StatusBadRequest)
			return
		}
		strategy = effectiveMergeStrategy(preferred, allowed)
	}
	if !config.ValidMergeStrategy(strategy) {
		http.Error(w, fmt.Sprintf("invalid strategy %q (want squash|merge|rebase)", strategy), http.StatusBadRequest)
		return
	}
	// Clamp an explicit strategy to what GitHub allows, if we know the allowed set.
	if len(allowed) > 0 {
		ok := false
		for _, a := range allowed {
			if a == strategy {
				ok = true
				break
			}
		}
		if !ok {
			http.Error(w, fmt.Sprintf("strategy %q is not allowed on this repo (GitHub allows: %v)", strategy, allowed), http.StatusBadRequest)
			return
		}
	}

	switch mode {
	case "plain":
		if err := svc.Merge(prID, ownerName, strategy); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeFilesJSON(w, map[string]any{"ok": true, "mode": "plain", "strategy": strategy})
	case "host":
		job, err := d.startMergeJob(prID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeFilesJSON(w, map[string]any{"ok": true, "mode": "host", "jobId": job.ID, "strategy": job.Strategy})
	case "sandbox":
		// Sandbox mode needs a project create + interactive start that the headless
		// API doesn't drive; direct the caller to the dashboard or host mode.
		http.Error(w,
			"sandbox merge is launched from the dashboard (it spins up a project). Use mode \"host\" for a headless background merge, or \"plain\" for a direct gh merge.",
			http.StatusBadRequest)
	}
}
