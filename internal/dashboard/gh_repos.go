package dashboard

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
	"strings"
)

// ghRepo is one entry from `gh repo list`.
type ghRepo struct {
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
	IsPrivate     bool   `json:"isPrivate"`
}

// handleGhRepos lists repositories the host `gh` CLI can access, for the
// create-project picker: the authenticated user's own repos PLUS the repos of
// every organization the user belongs to (a bare `gh repo list` returns only the
// user's own). Runs host-side with the operator's gh auth (same trust basis as
// the host shell); only name/url/visibility reach the browser — no tokens.
// GET /gh/repos
//
// If gh is missing or not authenticated, returns {available:false} so the UI can
// fall back to free-form URL/path entry rather than erroring. Org enumeration is
// best-effort: if it fails, the user's own repos are still returned.
func (d *dashboardServer) handleGhRepos(w http.ResponseWriter, r *http.Request) {
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "gh CLI not found on PATH"})
		return
	}

	// The user's own repos (bare `gh repo list`). If this fails, gh is unusable.
	own, err := ghRepoList(ghBin, "")
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "gh not authenticated or failed"})
		return
	}

	// Merge in each org's repos, deduped by nameWithOwner.
	seen := map[string]bool{}
	merged := make([]ghRepo, 0, len(own))
	addAll := func(rs []ghRepo) {
		for _, r := range rs {
			if !seen[r.NameWithOwner] {
				seen[r.NameWithOwner] = true
				merged = append(merged, r)
			}
		}
	}
	addAll(own)
	for _, org := range ghUserOrgs(ghBin) {
		if rs, err := ghRepoList(ghBin, org); err == nil {
			addAll(rs)
		}
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].NameWithOwner < merged[j].NameWithOwner })
	writeFilesJSON(w, map[string]any{"available": true, "repos": merged})
}

// ghRepoList runs `gh repo list [owner]` and returns the parsed repos. An empty
// owner lists the authenticated user's own repos.
func ghRepoList(ghBin, owner string) ([]ghRepo, error) {
	args := []string{"repo", "list"}
	if owner != "" {
		args = append(args, owner)
	}
	args = append(args, "--limit", "500", "--json", "nameWithOwner,url,isPrivate")
	out, err := exec.Command(ghBin, args...).Output()
	if err != nil {
		return nil, err
	}
	var repos []ghRepo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// handleGhBranches lists the branches of a repo for the create-project branch
// typeahead. GET /gh/branches?repo=<owner/name>. Uses `gh api
// repos/<owner/name>/branches`. Returns {available:false} on any failure so the
// UI can fall back to free-text branch entry.
func (d *dashboardServer) handleGhBranches(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	// Accept only "owner/name" so nothing arbitrary is spliced into the gh path.
	if !validOwnerName(repo) {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "invalid repo"})
		return
	}
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false})
		return
	}
	out, err := exec.Command(ghBin, "api",
		"repos/"+repo+"/branches", "--paginate", "--jq", ".[].name").Output()
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false})
		return
	}
	branches := strings.Fields(string(out))
	sort.Strings(branches)
	writeFilesJSON(w, map[string]any{"available": true, "branches": branches})
}

// validOwnerName reports whether s is a safe "owner/name" GitHub slug (the only
// shape we splice into a gh api path). Rejects empty, path traversal, extra
// segments, and anything outside GitHub's allowed characters.
func validOwnerName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
			if !ok {
				return false
			}
		}
	}
	return true
}

// ghUserOrgs returns the login names of orgs the authenticated user belongs to.
// Best-effort: returns nil on any failure (the picker still shows own repos).
func ghUserOrgs(ghBin string) []string {
	out, err := exec.Command(ghBin, "api", "user/orgs", "--jq", ".[].login").Output()
	if err != nil {
		return nil
	}
	// One login per line; Fields splits on whitespace/newlines and drops blanks.
	return strings.Fields(string(out))
}
