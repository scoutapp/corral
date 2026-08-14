package dashboard

import (
	"context"
	"strconv"

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

	runner := automations.NewRunner(automations.New(s), automationsRegistry())
	_, _ = runner.FireSecondary(ctx, event, automations.RunContext{RepoID: repoID, Vars: vars})
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
	runner := automations.NewRunner(automations.New(s), automationsRegistry())
	_, _ = runner.FireSecondary(ctx, automations.EventProjectStart, automations.RunContext{RepoID: repoID, Vars: vars})
}
