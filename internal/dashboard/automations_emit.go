package dashboard

import (
	"context"
	"strconv"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/prreview"
)

// Emit sites: where dashboard handlers fire automations event hooks after their
// built-in behavior runs. All PR-scoped events share one context builder so the
// {{var}} set (owner_name / pr_number / pr_url / pr_title / head_sha) is
// consistent across pr.approve, pr.analyze, pr.enter, etc. Firing is always
// best-effort — errors are recorded in run history, never surfaced — because the
// primary operation already succeeded by the time we get here.

// firePRHookEvent fires the secondary hooks bound to a PR-scoped event, building
// the run context from the stored PR. extraVars are merged in (e.g. the body /
// method a write action supplied). A no-op if no hooks are bound.
func (d *dashboardServer) firePRHookEvent(ctx context.Context, prID int64, event string, extraVars map[string]string) {
	if event == "" {
		return
	}
	s, err := d.getStore()
	if err != nil {
		return
	}
	svc := prreview.New(s)
	repoID, _ := svc.RepoIDForPR(prID)
	number, url, headSHA, title, err := svc.PRHookContext(prID)
	if err != nil {
		return
	}

	// Resolve the GitHub owner/name so bash/capability hooks have it too.
	ownerName := d.ownerNameForPR(svc, prID)

	vars := map[string]string{
		"owner_name": ownerName,
		"pr_number":  strconv.Itoa(number),
		"pr_url":     url,
		"pr_title":   title,
		"head_sha":   headSHA,
	}
	for k, v := range extraVars {
		vars[k] = v
	}

	runner := d.automationsRunner(automations.New(s))
	res, _ := runner.FireSecondary(ctx, event, automations.RunContext{RepoID: repoID, Vars: vars})
	d.logAutomationRun(ctx, event, repoID, res)
}

// logAutomationRun records an automation hook-chain execution in the app log,
// linking its run_id so the Logs tab can deep-link to the run detail. A no-op
// when no hooks fired (RunID 0 → nothing was recorded). Placed inside ctx's trace
// so it nests under whatever fired it (a PR action, a project start, a flow).
func (d *dashboardServer) logAutomationRun(ctx context.Context, event, repoID string, res automations.ChainResult) {
	if res.RunID == 0 {
		return
	}
	level := applog.LevelInfo
	if res.Status == automations.StatusError {
		level = applog.LevelError
	}
	d.applog().LogCtx(ctx, applog.Entry{
		Level: level, Category: applog.CatAutomation, Event: "automation." + event,
		Message: applog.Fmt("%s hooks — %d step(s), %s", event, len(res.Hooks), res.Status),
		RepoID:  repoID, Status: res.Status, RunID: res.RunID,
		Meta: map[string]any{"event": event, "steps": len(res.Hooks)},
	})
}

// fireProjectStartHooks fires project.start hooks for a launched project. It's
// not PR-scoped, so the context carries the workspace and — when the project was
// spawned from a PR/repo (its Source back-link) — the repo id + PR coordinates,
// letting a repo-scoped hook apply. Best-effort.
func (d *dashboardServer) fireProjectStartHooks(ctx context.Context, workspace string) {
	s, err := d.getStore()
	if err != nil {
		return
	}
	vars := map[string]string{"workspace": workspace}
	repoID := ""
	if cfg, err := readConfigForWorkspace(workspace); err == nil && cfg != nil && cfg.Source != nil {
		repoID = cfg.Source.RepoID
		if cfg.Source.Number > 0 {
			vars["pr_number"] = strconv.Itoa(cfg.Source.Number)
		}
		if cfg.Source.URL != "" {
			vars["pr_url"] = cfg.Source.URL
		}
	}
	runner := d.automationsRunner(automations.New(s))
	res, _ := runner.FireSecondary(ctx, automations.EventProjectStart, automations.RunContext{RepoID: repoID, Vars: vars})
	d.logAutomationRun(ctx, automations.EventProjectStart, repoID, res)
}
