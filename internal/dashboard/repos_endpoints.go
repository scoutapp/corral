package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
)

// handleRepos serves the repos list:
//
//	GET  /repos       — list repos
//	POST /repos       — add a repo {name?, url?, localPath?, isPrivate?} (clones the cache mirror)
func (d *dashboardServer) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := repos.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeFilesJSON(w, map[string]any{"repos": list})
	case http.MethodPost:
		var body struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			LocalPath string `json:"localPath"`
			IsPrivate bool   `json:"isPrivate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.URL) == "" && strings.TrimSpace(body.LocalPath) == "" {
			http.Error(w, "a url or localPath is required", http.StatusBadRequest)
			return
		}
		// Add clones the mirror, which can take a while / hit the network. Kept
		// synchronous for now (the UI shows a spinner); a progress stream is a
		// future enhancement.
		repo, err := repos.Add(repos.AddOptions{
			Name: body.Name, URL: body.URL, LocalPath: body.LocalPath, IsPrivate: body.IsPrivate,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeFilesJSON(w, map[string]any{"repo": repo})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRepoItem serves per-repo actions parsed from the path after "/repos/":
//
//	POST   /repos/<id>/fetch — refresh the cache mirror
//	DELETE /repos/<id>       — remove the repo + its cache
func (d *dashboardServer) handleRepoItem(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "fetch" && r.Method == http.MethodPost:
		if err := repos.Fetch(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		repo, _ := repos.Get(id)
		writeFilesJSON(w, map[string]any{"repo": repo})
	case action == "" && r.Method == http.MethodDelete:
		if err := repos.Remove(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeFilesJSON(w, map[string]any{"ok": true})
	// PR Review (see internal/prreview).
	case action == "forensics" && r.Method == http.MethodGet:
		d.handleRepoForensics(w, r, id)
	case action == "analyze" && r.Method == http.MethodPost:
		d.handleRepoAnalyze(w, r, id)
	case action == "analysis-status" && r.Method == http.MethodGet:
		d.handleRepoAnalysisStatus(w, r, id)
	case action == "prs" && r.Method == http.MethodGet:
		d.handleRepoPRs(w, r, id)
	case action == "prs/open" && r.Method == http.MethodGet:
		d.handleRepoOpenPRs(w, r, id)
	case action == "prs/fetch" && r.Method == http.MethodPost:
		d.handleRepoPRFetch(w, r, id)
	case action == "projects" && r.Method == http.MethodGet:
		d.handleRepoProjects(w, r, id)
	case action == "pin" && r.Method == http.MethodPost:
		d.handleRepoPin(w, r, id)
	case action == "color" && r.Method == http.MethodPost:
		d.handleRepoColor(w, r, id)
	case action == "merge-strategy" && r.Method == http.MethodGet:
		d.handleRepoMergeStrategyGet(w, r, id)
	case action == "merge-strategy" && r.Method == http.MethodPost:
		d.handleRepoMergeStrategySet(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// prreviewService opens the shared store and returns a PR Review service, or
// writes a 500 and returns nil if the DB can't be opened.
func (d *dashboardServer) prreviewService(w http.ResponseWriter, id string) *prreview.Service {
	if _, err := repos.Get(id); err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return nil
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return nil
	}
	return prreview.New(s)
}

// handleRepoForensics: GET /repos/<id>/forensics — per-file hot list.
func (d *dashboardServer) handleRepoForensics(w http.ResponseWriter, r *http.Request, id string) {
	svc := d.prreviewService(w, id)
	if svc == nil {
		return
	}
	stats, err := svc.Forensics(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"files": stats})
}

// handleRepoAnalyze: POST /repos/<id>/analyze — run git forensics over the
// repo's cache mirror and (re)populate pr_file_stats. Synchronous for now (the
// UI shows a spinner), mirroring how repo Add/Fetch are handled; a progress
// stream is a future enhancement.
func (d *dashboardServer) handleRepoAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	if repo.CachePath == "" {
		http.Error(w, "repo has no local cache to analyze", http.StatusBadRequest)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Forensics + tree-sitter callgraph. The callgraph is best-effort inside
	// AnalyzeRepo; forensics still returns if it fails (hotness falls back to
	// churn-only).
	res, err := prreview.New(s).AnalyzeRepo(r.Context(), id, repo.CachePath, repo.DefaultBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{
		"files":       res.Files,
		"cgNodes":     res.Nodes,
		"cgEdges":     res.Edges,
		"callgraphOk": res.CallgraphOK,
	})
}

// handleRepoPin: POST /repos/<id>/pin {pinned: bool} — pin/unpin a repo so it
// sorts to the top of the repos list.
func (d *dashboardServer) handleRepoPin(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Pinned bool `json:"pinned"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := repos.SetPinned(id, body.Pinned); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true, "pinned": body.Pinned})
}

// handleRepoColor: POST /repos/<id>/color {color: "#rrggbb"} — set a repo's
// label color, shown on the Repos and PRs pages.
func (d *dashboardServer) handleRepoColor(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !validHexColor(body.Color) {
		http.Error(w, "a hex color like #a05cff is required", http.StatusBadRequest)
		return
	}
	if err := repos.SetColor(id, body.Color); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true, "color": body.Color})
}

// validHexColor accepts #rgb or #rrggbb.
func validHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// handleRepoAnalysisStatus: GET /repos/<id>/analysis-status — whether the repo's
// stored analysis is current vs the mirror HEAD, and the new commits if not.
func (d *dashboardServer) handleRepoAnalysisStatus(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	st, err := prreview.New(s).AnalysisStatusFor(id, repo.CachePath, repo.DefaultBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, st)
}

// repoProject is a Corral project whose workspace git remote matches this repo.
type repoProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

// handleRepoProjects: GET /repos/<id>/projects — Corral sandbox projects started
// from this repo. Projects don't record their source repo, so we derive the
// link at query time: match each project workspace's `origin` owner/name against
// the repo's. Best-effort — a workspace without a git remote is simply skipped.
func (d *dashboardServer) handleRepoProjects(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	want := prreview.OwnerName(repo.URL)
	reg, err := readRegistry()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := []repoProject{}
	for _, p := range reg.Projects {
		if !workspaceMatchesRepo(p.Workspace, repo, want) {
			continue
		}
		out = append(out, repoProject{
			ID:        ProjectID(p.Workspace),
			Name:      filepath.Base(p.Workspace),
			Workspace: p.Workspace,
		})
	}
	writeFilesJSON(w, map[string]any{"projects": out})
}

// workspaceMatchesRepo reports whether a project workspace was cloned from repo.
// It compares the workspace's `origin` remote to the repo — by GitHub owner/name
// when the repo is a GitHub URL (want != ""), else by exact URL/localPath.
func workspaceMatchesRepo(workspace string, repo *repos.Repo, want string) bool {
	origin, err := gitOriginURL(workspace)
	if err != nil || origin == "" {
		return false
	}
	if want != "" {
		return prreview.OwnerName(origin) == want
	}
	// Non-GitHub source: match the raw remote against the repo's url or path.
	return origin == repo.URL || origin == repo.LocalPath
}

// gitOriginURL returns a workspace's origin remote URL, or "" if none.
func gitOriginURL(workspace string) (string, error) {
	cmd := exec.Command("git", "-C", workspace, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// handleRepoPRFetch: POST /repos/<id>/prs/fetch { "number": N } — fetch a PR's
// metadata + diff via `gh` and store it. Synchronous (UI shows a spinner).
func (d *dashboardServer) handleRepoPRFetch(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	ownerName := prreview.OwnerName(repo.URL)
	if ownerName == "" {
		http.Error(w, "repo is not a GitHub remote (need owner/name to fetch PRs)", http.StatusBadRequest)
		return
	}
	var body struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Number <= 0 {
		http.Error(w, "a positive PR number is required", http.StatusBadRequest)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	pr, err := svc.FetchPR(id, ownerName, body.Number)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// VIEW step (no AI): split the diff into hotness-ranked blocks using the
	// repo's already-built churn + callgraph. Passing a nil runner uses
	// deterministic placeholder titles; real Claude text is a separate, explicit
	// "enrich" step (POST /prs/<id>/enrich). Non-fatal — the PR + diff are stored.
	if _, err := svc.ExtractBlocks(r.Context(), pr.ID, nil); err != nil {
		log.Printf("prreview: block extraction for PR #%d failed: %v", pr.Number, err)
	}
	// The PR was viewed (page mount / re-view) — fire any pr.enter hooks.
	d.firePRHookEvent(r.Context(), pr.ID, automations.EventPREnter, nil)
	writeFilesJSON(w, map[string]any{"pr": pr})
}

// inboxPR is one open PR in the cross-project inbox, tagged with its repo.
type inboxPR struct {
	RepoID    string          `json:"repoId"`
	RepoName  string          `json:"repoName"`
	RepoColor string          `json:"repoColor"`
	PR        prreview.OpenPR `json:"pr"`
}

// handlePRInbox: GET /prs/inbox — open PRs aggregated across EVERY GitHub repo
// in the Repos list (a cross-repo review queue). One `gh pr list` per repo, run
// concurrently; repos without a GitHub remote are skipped. Best-effort per repo:
// a repo whose gh call fails is simply omitted.
func (d *dashboardServer) handlePRInbox(w http.ResponseWriter, r *http.Request) {
	repoList, err := repos.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type result struct {
		repo repos.Repo
		prs  []prreview.OpenPR
	}
	var wg sync.WaitGroup
	results := make([]result, len(repoList))
	for i := range repoList {
		repo := repoList[i]
		ownerName := prreview.OwnerName(repo.URL)
		if ownerName == "" {
			continue // not a GitHub remote
		}
		wg.Add(1)
		go func(idx int, repo repos.Repo, ownerName string) {
			defer wg.Done()
			prs, err := prreview.ListOpenPRs(ownerName, 100)
			if err != nil {
				return
			}
			results[idx] = result{repo: repo, prs: prs}
		}(i, repo, ownerName)
	}
	wg.Wait()

	out := []inboxPR{}
	for _, res := range results {
		for _, pr := range res.prs {
			out = append(out, inboxPR{
				RepoID: res.repo.ID, RepoName: res.repo.Name, RepoColor: res.repo.Color, PR: pr,
			})
		}
	}
	// currentUser lets the client offer a "Mine" tab (PRs I authored). Cached, so
	// this doesn't cost a gh call per poll. Empty when it can't be determined.
	writeFilesJSON(w, map[string]any{"prs": out, "currentUser": ghCurrentUser()})
}

// handlePRItem serves per-PR actions parsed from the path after "/prs/":
//
//	GET  /prs/<prId>/blocks   — hotness-ranked blocks for a fetched PR
//	POST /prs/<prId>/enrich   — add Claude analysis to the blocks + summary
//	GET  /prs/<prId>/risk     — stored risk verdict (or {risk:null})
//	POST /prs/<prId>/analyze  — (re)compute the risk verdict via claude
//	GET  /prs/<prId>/chat/ws  — block-scoped streaming chat
func (d *dashboardServer) handlePRItem(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	prID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "chat/ws" && r.Method == http.MethodGet:
		d.handleBlockChatWS(w, r, prID)
		return
	case action == "blocks" && r.Method == http.MethodGet:
		d.handlePRBlocks(w, r, prID)
	case action == "file-stats" && r.Method == http.MethodGet:
		d.handlePRFileStats(w, r, prID)
	case action == "enrich" && r.Method == http.MethodPost:
		d.handlePREnrich(w, r, prID)
	case action == "rerank" && r.Method == http.MethodPost:
		d.handlePRRerank(w, r, prID)
	case action == "approve" && r.Method == http.MethodPost:
		d.handlePRAction(w, r, prID, "approve")
	case action == "request-changes" && r.Method == http.MethodPost:
		d.handlePRAction(w, r, prID, "request-changes")
	case action == "comment" && r.Method == http.MethodPost:
		d.handlePRAction(w, r, prID, "comment")
	case action == "merge" && r.Method == http.MethodPost:
		d.handlePRAction(w, r, prID, "merge")
	case action == "merge-prompt" && r.Method == http.MethodGet:
		d.handlePRMergePrompt(w, r, prID)
	case action == "merge-watch" && r.Method == http.MethodPost:
		d.handlePRMergeWatch(w, r, prID)
	case action == "merge-host/start" && r.Method == http.MethodPost:
		d.handlePRMergeHostStart(w, r, prID)
	case action == "merge-host/ws" && r.Method == http.MethodGet:
		d.handlePRMergeHostWS(w, r, prID)
	case action == "line-comment" && r.Method == http.MethodPost:
		d.handlePRLineComment(w, r, prID)
	case action == "risk" && r.Method == http.MethodGet:
		d.handlePRRiskGet(w, r, prID)
	case action == "analyze" && r.Method == http.MethodPost:
		d.handlePRAnalyze(w, r, prID)
	case action == "issues" && r.Method == http.MethodGet:
		d.handlePRIssues(w, r, prID)
	case action == "links" && r.Method == http.MethodGet:
		d.handlePRLinksGet(w, r, prID)
	case action == "links" && r.Method == http.MethodPost:
		d.handlePRLinkAdd(w, r, prID)
	case action == "links/suggest" && r.Method == http.MethodGet:
		d.handlePRLinkSuggest(w, r, prID)
	case strings.HasPrefix(action, "links/") && r.Method == http.MethodDelete:
		d.handlePRLinkRemove(w, r, strings.TrimPrefix(action, "links/"))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePRIssues: GET /prs/<prId>/issues — the issue(s) this PR closes, for the
// PR-view Issue description tab(s). Resolves via GitHub's closingIssuesReferences,
// falling back to a number in the head branch name. A PR that closes nothing
// returns {"issues": []} (the UI then shows just the PR description, no tabs).
func (d *dashboardServer) handlePRIssues(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	ownerName := d.ownerNameForPR(svc, prID)
	if ownerName == "" {
		// Not a GitHub remote — no issues to link. Empty, not an error.
		writeJSON(w, map[string]any{"issues": []prreview.LinkedIssue{}})
		return
	}
	issues, err := svc.LinkedIssues(prID, ownerName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if issues == nil {
		issues = []prreview.LinkedIssue{}
	}
	writeJSON(w, map[string]any{"issues": issues})
}

func (d *dashboardServer) handlePRLinksGet(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	links, err := prreview.New(s).Links(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"links": links})
}

func (d *dashboardServer) handlePRLinkSuggest(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sug, err := prreview.New(s).SuggestLinks(prID, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"suggestions": sug})
}

func (d *dashboardServer) handlePRLinkAdd(w http.ResponseWriter, r *http.Request, prID int64) {
	var body struct {
		LinkedPrId   int64  `json:"linkedPrId"`
		Relationship string `json:"relationship"`
		Note         string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.LinkedPrId <= 0 {
		http.Error(w, "linkedPrId is required", http.StatusBadRequest)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	link, err := prreview.New(s).AddLink(prID, body.LinkedPrId, body.Relationship, body.Note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"link": link})
}

func (d *dashboardServer) handlePRLinkRemove(w http.ResponseWriter, r *http.Request, linkIDStr string) {
	linkID, err := strconv.ParseInt(linkIDStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := prreview.New(s).RemoveLink(linkID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

func (d *dashboardServer) handlePRBlocks(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	blocks, err := svc.Blocks(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, _ := svc.BlocksStatusFor(prID) // best-effort freshness signal
	writeFilesJSON(w, map[string]any{"blocks": blocks, "status": status})
}

// ownerNameForPR resolves a stored PR's GitHub "owner/name" from its repo's
// remote URL. Returns "" if the repo/URL isn't a GitHub remote.
func (d *dashboardServer) ownerNameForPR(s *prreview.Service, prID int64) string {
	repoID, err := s.RepoIDForPR(prID)
	if err != nil {
		return ""
	}
	repo, err := repos.Get(repoID)
	if err != nil {
		return ""
	}
	return prreview.OwnerName(repo.URL)
}

// handlePRAction runs a write action (approve / request-changes / comment /
// merge) against GitHub via `gh`. Body is JSON: {body?, method?}.
func (d *dashboardServer) handlePRAction(w http.ResponseWriter, r *http.Request, prID int64, action string) {
	var body struct {
		Body   string `json:"body"`
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

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

	switch action {
	case "approve":
		err = svc.Approve(prID, ownerName, body.Body)
	case "request-changes":
		err = svc.RequestChanges(prID, ownerName, body.Body)
	case "comment":
		err = svc.Comment(prID, ownerName, body.Body)
	case "merge":
		err = svc.Merge(prID, ownerName, body.Method)
	}
	repoID, _ := svc.RepoIDForPR(prID)
	num, _, _, _, _ := svc.PRHookContext(prID)
	if err != nil {
		d.applog().ErrorfCtx(r.Context(), applog.CatPRAction, "pr."+action,
			applog.Fmt("PR action %q on %s#%d failed", action, ownerName, num), err,
			map[string]any{"owner": ownerName, "pr": num, "action": action})
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	d.applog().InfoCtx(r.Context(), applog.Entry{
		Category: applog.CatPRAction, Event: "pr." + action,
		Message: applog.Fmt("%s %s#%d", prActionVerb(action), ownerName, num),
		RepoID:  repoID, Status: applog.StatusOK,
		Meta: map[string]any{"owner": ownerName, "pr": num, "action": action},
	})

	// The built-in gh action (the PRIMARY) succeeded. Fire any user-configured
	// secondary hooks bound to this event — best-effort: their success/failure is
	// recorded but never changes the 200 we return here. This is what makes
	// "Approve" also ping Slack, etc., additively.
	d.firePRActionHooks(r.Context(), svc, prID, action, ownerName, body.Body, body.Method)

	writeFilesJSON(w, map[string]any{"ok": true})
}

// prActionVerb renders a PR action name as a past-tense log verb.
func prActionVerb(action string) string {
	switch action {
	case "approve":
		return "Approved"
	case "request-changes":
		return "Requested changes on"
	case "comment":
		return "Commented on"
	case "merge":
		return "Merged"
	}
	return action
}

// prActionEvent maps a PR write-action name to its automations event.
func prActionEvent(action string) string {
	switch action {
	case "approve":
		return automations.EventPRApprove
	case "request-changes":
		return automations.EventPRRequestChanges
	case "comment":
		return automations.EventPRComment
	case "merge":
		return automations.EventPRMerge
	}
	return ""
}

// firePRActionHooks fires the secondary automations hooks for a PR write action
// whose built-in behavior already ran. It delegates to the shared PR-event
// emitter, adding the write action's body/method vars.
func (d *dashboardServer) firePRActionHooks(ctx context.Context, _ *prreview.Service, prID int64, action, ownerName, body, method string) {
	_ = ownerName // owner is re-resolved inside firePRHookEvent
	d.firePRHookEvent(ctx, prID, prActionEvent(action), map[string]string{
		"body":   body,
		"method": method,
	})
}

// handlePRLineComment posts a review comment on a diff line.
func (d *dashboardServer) handlePRLineComment(w http.ResponseWriter, r *http.Request, prID int64) {
	var body struct {
		Body string `json:"body"`
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" || body.Line <= 0 {
		http.Error(w, "path and line are required", http.StatusBadRequest)
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
	if err := svc.LineComment(prID, ownerName, body.Body, body.Path, body.Line, body.Side); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// handlePRRerank: POST /prs/<id>/rerank — recompute block hotness against the
// repo's current churn + callgraph, preserving existing AI analysis (no Claude
// calls). Used when a repo was (re)analyzed after the PR's blocks were ranked.
func (d *dashboardServer) handlePRRerank(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	blocks, err := svc.Rerank(r.Context(), prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, _ := svc.BlocksStatusFor(prID)
	writeFilesJSON(w, map[string]any{"blocks": blocks, "status": status})
}

// handlePRFileStats: GET /prs/<id>/file-stats — rich per-file forensics for the
// files this PR touches (fix ratio, author diversity, staleness, velocity,
// callgraph ref count).
func (d *dashboardServer) handlePRFileStats(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := prreview.New(s).FileForensics(prID, time.Now().Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"files": stats})
}

// handlePREnrich: POST /prs/<id>/enrich — the ENRICH step. Re-extracts the PR's
// blocks WITH the host `claude` CLI so each block gets an AI title/explanation/
// codebase-context/edge-cases and the PR gets a <=100-char summary. Requires
// claude; returns 502 if it isn't resolvable (blocks already exist from View).
func (d *dashboardServer) handlePREnrich(w http.ResponseWriter, r *http.Request, prID int64) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, "the `claude` CLI could not be located — install Claude Code and restart the dashboard", http.StatusBadGateway)
		return
	}
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)
	repoID, _ := svc.RepoIDForPR(prID)
	num, _, _, _, _ := svc.PRHookContext(prID)

	// Timed AI span (the claude CLI call has real wall-clock). Anything it triggers
	// — the pr.analyze hooks below — nests under this span in the trace.
	ctx, endSpan := d.applog().StartSpan(r.Context(), applog.Entry{
		Category: applog.CatAI, Event: "ai.analyze",
		Message: applog.Fmt("Analyze PR #%d", num),
		RepoID:  repoID, Meta: map[string]any{"pr": num},
	})
	blocks, err := svc.WithPromptResolver(d.promptResolver()).ExtractBlocks(ctx, prID, prreview.NewClaudeRunner(claudeBin))
	if err != nil {
		endSpan(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	endSpan(nil)
	// The built-in AI enrichment (primary) succeeded — fire any pr.analyze hooks.
	d.firePRHookEvent(ctx, prID, automations.EventPRAnalyze, nil)
	writeFilesJSON(w, map[string]any{"blocks": blocks})
}

// handlePRRiskGet returns the last stored risk verdict (null if never analyzed).
func (d *dashboardServer) handlePRRiskGet(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v, err := prreview.New(s).StoredRisk(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"risk": v})
}

// handlePRAnalyze (re)computes the PR risk verdict via the host claude CLI.
func (d *dashboardServer) handlePRAnalyze(w http.ResponseWriter, r *http.Request, prID int64) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	claudeBin, _ := resolveClaudeBin()
	rsvc := prreview.New(s)
	rRepoID, _ := rsvc.RepoIDForPR(prID)
	rNum, _, _, _, _ := rsvc.PRHookContext(prID)

	ctx, endSpan := d.applog().StartSpan(r.Context(), applog.Entry{
		Category: applog.CatAI, Event: "ai.risk",
		Message: applog.Fmt("Risk assessment PR #%d", rNum),
		RepoID:  rRepoID, Meta: map[string]any{"pr": rNum},
	})
	v, err := rsvc.WithPromptResolver(d.promptResolver()).AnalyzeRisk(ctx, prID, prreview.NewClaudeRunner(claudeBin))
	if err != nil {
		endSpan(err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	endSpan(nil)
	writeFilesJSON(w, map[string]any{"risk": v})
}

// handleRepoOpenPRs: GET /repos/<id>/prs/open — the repo's live open PRs via
// `gh pr list`. A live GitHub read (no DB); returns {available:false} if the
// repo isn't a GitHub remote or gh fails, so the UI can degrade to manual fetch.
func (d *dashboardServer) handleRepoOpenPRs(w http.ResponseWriter, r *http.Request, id string) {
	repo, err := repos.Get(id)
	if err != nil {
		http.Error(w, "unknown repo", http.StatusNotFound)
		return
	}
	ownerName := prreview.OwnerName(repo.URL)
	if ownerName == "" {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "not a GitHub remote"})
		return
	}
	prs, err := prreview.ListOpenPRs(ownerName, 100)
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": err.Error()})
		return
	}
	writeFilesJSON(w, map[string]any{"available": true, "prs": prs})
}

// handleRepoPRs: GET /repos/<id>/prs — fetched pull requests for the repo.
func (d *dashboardServer) handleRepoPRs(w http.ResponseWriter, r *http.Request, id string) {
	svc := d.prreviewService(w, id)
	if svc == nil {
		return
	}
	prs, err := svc.PRs(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"prs": prs})
}
