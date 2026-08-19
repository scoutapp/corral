package dashboard

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
	"github.com/scoutapp/corral/internal/session"
)

// handlePRMergePrompt returns everything the frontend needs to launch a
// "rebase & merge in sandbox" job for a PR: the rendered pr.merge prompt (with
// the effective merge strategy substituted in), the repo id + PR branch to
// check out, and the resolved strategy. The frontend then creates a one-shot
// project on the branch, starts it, submits this prompt, and calls
// /prs/<id>/merge-watch to arm the poll-and-teardown watcher.
//
//	GET /prs/<id>/merge-prompt
func (d *dashboardServer) handlePRMergePrompt(w http.ResponseWriter, r *http.Request, prID int64) {
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
	mi, err := svc.PRMergeInfo(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Repo display name + default (base) branch, best-effort from the cache.
	repoName, defaultBranch := ownerName, "main"
	if repo, gerr := repos.Get(repoID); gerr == nil {
		if repo.Name != "" {
			repoName = repo.Name
		}
		if repo.DefaultBranch != "" {
			defaultBranch = repo.DefaultBranch
		}
	}

	allowed, preferred := d.resolveRepoMergeMethods(repoID, ownerName)
	strategy := effectiveMergeStrategy(preferred, allowed)

	prompt := d.renderMergePrompt(repoID, prPromptSlots(repoName, mi, strategy, defaultBranch))

	writeFilesJSON(w, map[string]any{
		"prompt":         prompt,
		"repo_id":        repoID,
		"branch":         mi.HeadRef,
		"strategy":       strategy,  // the resolved default, already clamped to allowed
		"allowed":        allowed,   // methods GitHub permits (for the picker)
		"preferred":      preferred, // this repo's stored preference ("" = none)
		"repo_name":      repoName,
		"default_branch": defaultBranch,
		"pr_number":      mi.Number,
	})
}

// resolveRepoMergeMethods returns (allowed, preferred) for a repo: the merge
// methods GitHub permits and the repo's stored preference. The allowed set is
// cached on the repo record; if it's missing, we fetch it from GitHub once and
// persist it (best-effort). An empty allowed result means "unknown" — callers
// then treat all three as available.
func (d *dashboardServer) resolveRepoMergeMethods(repoID, ownerName string) (allowed []string, preferred string) {
	repo, err := repos.Get(repoID)
	if err != nil {
		return nil, ""
	}
	preferred = repo.PreferredMergeStrategy
	allowed = repo.AllowedMergeStrategies
	if len(allowed) == 0 {
		if fetched := ghAllowedMergeMethods(ownerName); len(fetched) > 0 {
			allowed = fetched
			_ = repos.SetAllowedMergeStrategies(repoID, fetched) // cache; ignore write error
		}
	}
	return allowed, preferred
}

// effectiveMergeStrategy resolves the merge method to use: the repo preference
// if set, else the global default, else "squash" — then clamped to the allowed
// set (if the resolved choice isn't permitted by GitHub, fall back to the first
// allowed method). An empty allowed set (unknown) imposes no clamp.
func effectiveMergeStrategy(preferred string, allowed []string) string {
	strategy := preferred
	if !config.ValidMergeStrategy(strategy) {
		strategy = config.ReadGlobalSettings().MergeStrategyOrDefault()
	}
	if len(allowed) == 0 {
		return strategy // unknown allow-set → don't clamp
	}
	for _, a := range allowed {
		if a == strategy {
			return strategy
		}
	}
	return allowed[0] // resolved choice is disabled on the repo → first allowed
}

// prPromptSlots builds the {{slot}} set for the pr.merge prompt.
func prPromptSlots(repoName string, mi prreview.MergeInfo, strategy, defaultBranch string) map[string]string {
	return map[string]string{
		"repo":           repoName,
		"branch":         mi.HeadRef,
		"strategy":       strategy,
		"pr_number":      strconv.Itoa(mi.Number),
		"pr_title":       mi.Title,
		"pr_url":         mi.URL,
		"default_branch": defaultBranch,
	}
}

// renderMergePrompt resolves the effective pr.merge template (built-in → global
// → repo override) and fills its slots, falling back to the built-in default so
// a prompt is always produced.
func (d *dashboardServer) renderMergePrompt(repoID string, slots map[string]string) string {
	def, _ := automations.PromptDefFor(automations.PromptPRMerge)
	prompt := automations.RenderTemplate(def.Default, slots)
	if s, err := d.getStore(); err == nil {
		if rendered := automations.New(s).RenderPrompt(automations.PromptPRMerge, repoID, slots); strings.TrimSpace(rendered) != "" {
			prompt = rendered
		}
	}
	return strings.TrimSpace(prompt)
}

// handleRepoMergeStrategyGet returns a repo's merge-strategy state for its
// settings UI: the methods GitHub allows, the repo's stored preference, the
// global default, and the resolved effective strategy (what a merge would use).
//
//	GET /repos/<id>/merge-strategy
func (d *dashboardServer) handleRepoMergeStrategyGet(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ownerName := prreview.OwnerName(repo.URL)
	allowed, preferred := d.resolveRepoMergeMethods(id, ownerName)

	gs := config.ReadGlobalSettings()
	writeFilesJSON(w, map[string]any{
		"allowed":        allowed,          // GitHub-permitted methods ([] = unknown → all)
		"preferred":      preferred,        // repo preference ("" = none set)
		"global_default": gs.MergeStrategy, // "" when the user never set a global default
		"effective":      effectiveMergeStrategy(preferred, allowed),
	})
}

// handleRepoMergeStrategySet saves a repo's preferred merge method (per-repo
// preference). An empty string clears it (fall back to the global/ask default).
//
//	POST /repos/<id>/merge-strategy   { "strategy": "squash"|"merge"|"rebase"|"" }
func (d *dashboardServer) handleRepoMergeStrategySet(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Strategy string `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := repos.SetPreferredMergeStrategy(id, strings.TrimSpace(body.Strategy)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true, "preferred": body.Strategy})
}

// handlePRMergeWatch arms a background watcher for a merge-in-sandbox job: it
// polls the PR's merge state on GitHub and, once the PR is MERGED, tears down
// the one-shot sandbox (stop container + de-register project) if auto-teardown
// is enabled. The watcher runs detached so it survives the browser navigating
// away — the whole point of doing teardown server-side.
//
//	POST /prs/<id>/merge-watch   { "projectId": "<id>" }
func (d *dashboardServer) handlePRMergeWatch(w http.ResponseWriter, r *http.Request, prID int64) {
	var body struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ProjectID) == "" {
		http.Error(w, "projectId is required", http.StatusBadRequest)
		return
	}

	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	ownerName := d.ownerNameForPR(svc, prID)
	if ownerName == "" {
		http.Error(w, "repo is not a GitHub remote", http.StatusBadRequest)
		return
	}

	autoTeardown := config.ReadGlobalSettings().MergeAutoTeardownOn()
	num, _, _, _, _ := svc.PRHookContext(prID)

	go d.watchMergeAndTeardown(svc, prID, num, ownerName, body.ProjectID, autoTeardown)

	writeFilesJSON(w, map[string]any{"ok": true, "watching": true, "auto_teardown": autoTeardown})
}

// mergeWatchPoll / mergeWatchTimeout bound the teardown watcher: check every
// 30s, give up after 6h (a merge that hasn't landed by then is stuck on a
// human — the sandbox stays for inspection). Vars (not consts) so tests can
// shorten them.
var (
	mergeWatchPoll    = 30 * time.Second
	mergeWatchTimeout = 6 * time.Hour
)

// watchMergeAndTeardown polls the PR's merge state until it's MERGED (or the
// timeout elapses), then — if autoTeardown — stops the sandbox container and
// de-registers the one-shot project. Best-effort and self-contained: it owns
// its own logging and never touches the HTTP response.
func (d *dashboardServer) watchMergeAndTeardown(svc *prreview.Service, prID int64, prNum int, ownerName, projectID string, autoTeardown bool) {
	deadline := time.Now().Add(mergeWatchTimeout)
	for {
		ms, err := svc.PRMergeState(prID, ownerName)
		if err == nil && ms.Merged {
			d.applog().Log(applog.Entry{
				Category: applog.CatPRAction, Event: "pr.merge_sandbox.merged",
				Message: applog.Fmt("PR %s#%d merged; %s sandbox", ownerName, prNum,
					map[bool]string{true: "tearing down", false: "leaving"}[autoTeardown]),
				Status: applog.StatusOK,
				Meta:   map[string]any{"owner": ownerName, "pr": prNum, "project": projectID, "auto_teardown": autoTeardown},
			})
			if autoTeardown {
				d.teardownMergeSandbox(projectID)
			}
			return
		}
		if time.Now().After(deadline) {
			d.applog().Log(applog.Entry{
				Category: applog.CatPRAction, Event: "pr.merge_sandbox.timeout",
				Message: applog.Fmt("PR %s#%d not merged within watch window; leaving sandbox %s", ownerName, prNum, projectID),
				Status:  applog.StatusOK,
				Meta:    map[string]any{"owner": ownerName, "pr": prNum, "project": projectID},
			})
			return
		}
		time.Sleep(mergeWatchPoll)
	}
}

// teardownMergeSandbox stops a one-shot merge project's container and removes it
// from the registry — the server-side inverse of the frontend's create+start.
// Mirrors handleStopProject's teardown (docker rm -f + kill tmux session), then
// drops the registry entry so the project disappears from the UI. Best-effort.
func (d *dashboardServer) teardownMergeSandbox(projectID string) {
	workspace, err := lookupWorkspaceByID(projectID)
	if err != nil {
		return
	}
	container := session.ContainerNameForWorkspace(workspace)
	tmuxSession := session.TmuxSessionNameForWorkspace(workspace)
	_ = exec.Command("docker", "rm", "-f", container).Run()
	_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()
	d.deregisterProject(projectID)
}

// deregisterProject drops a project from the registry by id (the registry-only
// half of removal). Reused by the merge-sandbox teardown.
func (d *dashboardServer) deregisterProject(projectID string) {
	reg, err := readRegistry()
	if err != nil {
		return
	}
	kept := reg.Projects[:0]
	for _, p := range reg.Projects {
		if ProjectID(p.Workspace) == projectID {
			continue
		}
		kept = append(kept, p)
	}
	reg.Projects = kept
	_ = writeRegistry(reg)
}
