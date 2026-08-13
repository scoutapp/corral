package dashboard

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
	case action == "prs" && r.Method == http.MethodGet:
		d.handleRepoPRs(w, r, id)
	case action == "prs/open" && r.Method == http.MethodGet:
		d.handleRepoOpenPRs(w, r, id)
	case action == "prs/fetch" && r.Method == http.MethodPost:
		d.handleRepoPRFetch(w, r, id)
	case action == "projects" && r.Method == http.MethodGet:
		d.handleRepoProjects(w, r, id)
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
	// Extract hotness-ranked blocks + an AI summary from the fetched diff. Uses
	// the host `claude` CLI if resolvable; otherwise ExtractBlocks falls back to
	// deterministic placeholders (blocks still created, summary = PR title). A
	// block-extraction failure is non-fatal — the PR + diff are already stored.
	claudeBin, _ := resolveClaudeBin()
	if _, err := svc.ExtractBlocks(r.Context(), pr.ID, prreview.NewClaudeRunner(claudeBin)); err != nil {
		log.Printf("prreview: block extraction for PR #%d failed: %v", pr.Number, err)
	}
	// Re-read so the response carries the AI-generated short summary.
	if prs, e := svc.PRs(id); e == nil {
		for i := range prs {
			if prs[i].ID == pr.ID {
				pr = &prs[i]
				break
			}
		}
	}
	writeFilesJSON(w, map[string]any{"pr": pr})
}

// handlePRItem serves per-PR actions parsed from the path after "/prs/":
//
//	GET  /prs/<prId>/blocks   — hotness-ranked blocks for a fetched PR
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
	case action == "risk" && r.Method == http.MethodGet:
		d.handlePRRiskGet(w, r, prID)
	case action == "analyze" && r.Method == http.MethodPost:
		d.handlePRAnalyze(w, r, prID)
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
	blocks, err := prreview.New(s).Blocks(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	v, err := prreview.New(s).AnalyzeRisk(r.Context(), prID, prreview.NewClaudeRunner(claudeBin))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
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
