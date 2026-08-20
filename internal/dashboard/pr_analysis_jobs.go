package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/prreview"
)

// The analysis-job tracker backs the FIRE-AND-RETURN AI analysis API: enrich
// (per-block "Analyze with AI") and risk (the PR-level verdict) both spawn host
// Claude and take real wall-clock, so the /api routes start them in the
// background and return immediately with a status the caller polls. The browser
// routes stay synchronous (the UI shows its own progress).

// Analysis-job status values.
const (
	analysisIdle    = "idle"
	analysisRunning = "running"
	analysisDone    = "done"
	analysisFailed  = "failed"
)

// analysisJobState is the tracked state of one (prID, kind) run.
type analysisJobState struct {
	Status string `json:"status"` // idle | running | done | failed
	Error  string `json:"error,omitempty"`
}

// analysisJobTracker holds per-(prID,kind) run state. Kinds: "enrich", "risk".
type analysisJobTracker struct {
	mu   sync.Mutex
	jobs map[string]*analysisJobState
}

func newAnalysisJobTracker() *analysisJobTracker {
	return &analysisJobTracker{jobs: map[string]*analysisJobState{}}
}

func analysisKey(prID int64, kind string) string { return fmt.Sprintf("%d:%s", prID, kind) }

// state returns the current state for a (prID, kind), defaulting to idle.
func (t *analysisJobTracker) state(prID int64, kind string) analysisJobState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.jobs[analysisKey(prID, kind)]; s != nil {
		return *s
	}
	return analysisJobState{Status: analysisIdle}
}

// begin marks a job running unless it already is. Returns false if a run is
// already in progress (so we don't double-spawn Claude for the same work).
func (t *analysisJobTracker) begin(prID int64, kind string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := analysisKey(prID, kind)
	if s := t.jobs[k]; s != nil && s.Status == analysisRunning {
		return false
	}
	t.jobs[k] = &analysisJobState{Status: analysisRunning}
	return true
}

// finish records a terminal state for a job.
func (t *analysisJobTracker) finish(prID int64, kind string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := &analysisJobState{Status: analysisDone}
	if err != nil {
		st.Status = analysisFailed
		st.Error = err.Error()
	}
	t.jobs[analysisKey(prID, kind)] = st
}

// startEnrich kicks off the per-block AI enrichment in the background. Returns an
// error only for a synchronous precondition (no claude, already running); the
// analysis itself completes asynchronously and its result is polled.
func (d *dashboardServer) startEnrich(prID, parentConvID int64) error {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		return fmt.Errorf("the `claude` CLI could not be located — install Claude Code and restart the dashboard")
	}
	if !d.analysisJobs.begin(prID, "enrich") {
		return nil // already running — idempotent start
	}
	go func() {
		s, err := d.getStore()
		if err != nil {
			d.analysisJobs.finish(prID, "enrich", err)
			return
		}
		svc := prreview.New(s)
		repoID, _ := svc.RepoIDForPR(prID)
		num, _, _, _, _ := svc.PRHookContext(prID)
		ctx, endSpan := d.applog().StartSpan(context.Background(), applog.Entry{
			Category: applog.CatAI, Event: "ai.analyze",
			Message: applog.Fmt("Analyze PR #%d (api)", num),
			RepoID:  repoID, Meta: map[string]any{"pr": num, "via": "api"},
		})
		runner := d.capturingRunner(ctx, convOrigin{
			Kind: "analysis", OriginID: fmt.Sprintf("enrich-%d", prID), RepoID: repoID, PRNumber: num,
			ParentConversationID: parentConvID, // chain to the chat that triggered this (if any)
		}, prreview.NewClaudeRunner(claudeBin))
		_, aerr := svc.WithPromptResolver(d.promptResolver()).ExtractBlocks(ctx, prID, runner)
		endSpan(aerr)
		if aerr == nil {
			d.firePRHookEvent(ctx, prID, automations.EventPRAnalyze, nil)
		}
		d.analysisJobs.finish(prID, "enrich", aerr)
	}()
	return nil
}

// startRisk kicks off the PR-level risk verdict in the background.
func (d *dashboardServer) startRisk(prID, parentConvID int64) error {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		return fmt.Errorf("the `claude` CLI could not be located — install Claude Code and restart the dashboard")
	}
	if !d.analysisJobs.begin(prID, "risk") {
		return nil
	}
	go func() {
		s, err := d.getStore()
		if err != nil {
			d.analysisJobs.finish(prID, "risk", err)
			return
		}
		svc := prreview.New(s)
		repoID, _ := svc.RepoIDForPR(prID)
		num, _, _, _, _ := svc.PRHookContext(prID)
		ctx, endSpan := d.applog().StartSpan(context.Background(), applog.Entry{
			Category: applog.CatAI, Event: "ai.risk",
			Message: applog.Fmt("Risk assessment PR #%d (api)", num),
			RepoID:  repoID, Meta: map[string]any{"pr": num, "via": "api"},
		})
		runner := d.capturingRunner(ctx, convOrigin{
			Kind: "analysis", OriginID: fmt.Sprintf("risk-%d", prID), RepoID: repoID, PRNumber: num,
			ParentConversationID: parentConvID, // chain to the chat that triggered this (if any)
		}, prreview.NewClaudeRunner(claudeBin))
		_, aerr := svc.WithPromptResolver(d.promptResolver()).AnalyzeRisk(ctx, prID, runner)
		endSpan(aerr)
		d.analysisJobs.finish(prID, "risk", aerr)
	}()
	return nil
}

// handleAPIPREnrich: POST /api/prs/<id>/enrich — start the per-block AI analysis
// in the background; returns immediately. Poll GET /api/prs/<id>/analysis for
// completion, then read GET /api/prs/<id>/blocks.
func (d *dashboardServer) handleAPIPREnrich(w http.ResponseWriter, r *http.Request, prID int64) {
	if err := d.startEnrich(prID, parentConvFromRequest(r)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true, "kind": "enrich", "status": d.analysisJobs.state(prID, "enrich").Status})
}

// handleAPIPRRiskStart: POST /api/prs/<id>/analyze — start the risk verdict in
// the background; poll analysis, then read GET /api/prs/<id>/risk.
func (d *dashboardServer) handleAPIPRRiskStart(w http.ResponseWriter, r *http.Request, prID int64) {
	if err := d.startRisk(prID, parentConvFromRequest(r)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true, "kind": "risk", "status": d.analysisJobs.state(prID, "risk").Status})
}

// handleAPIPRAnalysisStatus: GET /api/prs/<id>/analysis — the enrich + risk job
// states, so a caller knows when a fired analysis has finished.
func (d *dashboardServer) handleAPIPRAnalysisStatus(w http.ResponseWriter, r *http.Request, prID int64) {
	writeFilesJSON(w, map[string]any{
		"enrich": d.analysisJobs.state(prID, "enrich"),
		"risk":   d.analysisJobs.state(prID, "risk"),
	})
}
