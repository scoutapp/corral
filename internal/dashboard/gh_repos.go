package dashboard

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
)

// ghRepo is one entry from `gh repo list`.
type ghRepo struct {
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
	IsPrivate     bool   `json:"isPrivate"`
}

// handleGhRepos lists repositories the host `gh` CLI currently has access to, for
// the create-project picker. Runs host-side with the operator's gh auth (same
// trust basis as the host shell); no tokens are exposed to the browser — only
// name/url/visibility. GET /gh/repos[?limit=N]
//
// If gh is missing or not authenticated, returns {available:false} so the UI can
// fall back to free-form URL/path entry rather than erroring.
func (d *dashboardServer) handleGhRepos(w http.ResponseWriter, r *http.Request) {
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "gh CLI not found on PATH"})
		return
	}
	// A generous cap; the picker is searchable client-side.
	cmd := exec.Command(ghBin, "repo", "list",
		"--limit", "200",
		"--json", "nameWithOwner,url,isPrivate")
	out, err := cmd.Output()
	if err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "gh not authenticated or failed"})
		return
	}
	var repos []ghRepo
	if err := json.Unmarshal(out, &repos); err != nil {
		writeFilesJSON(w, map[string]any{"available": false, "reason": "unexpected gh output"})
		return
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].NameWithOwner < repos[j].NameWithOwner })
	writeFilesJSON(w, map[string]any{"available": true, "repos": repos})
}
