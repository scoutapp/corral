package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/project"
	"github.com/jackrothrock/sandclaude/internal/repos"
)

// managedWorkspacesDir is where the dashboard puts workspaces it creates (new
// dirs and clones), so it owns their lifecycle. "Add existing dir" points in place.
func managedWorkspacesDir() string {
	return filepath.Join(config.SandclaudeHome(), "workspaces")
}

var wsSlugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// uniqueWorkspacePath returns a fresh managed workspace path for a name, adding a
// numeric suffix if needed so concurrent same-repo spin-offs never collide.
func uniqueWorkspacePath(name string) string {
	base := strings.Trim(wsSlugRe.ReplaceAllString(name, "-"), "-.")
	if base == "" {
		base = "project"
	}
	dir := filepath.Join(managedWorkspacesDir(), base)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir
		}
		dir = filepath.Join(managedWorkspacesDir(), fmt.Sprintf("%s-%d", base, i))
	}
}

// handleCreateProject creates a project from the dashboard and registers it.
//
//	POST /projects/create
//	{ "mode": "existing"|"new"|"clone", ...mode-specific fields, ...init opts }
//
// existing: { path }             — register an existing dir in place.
// new:      { name }             — mkdir a fresh managed workspace.
// clone:    { repoId, name?, branch? } — clone --local from the repo's cache.
//
// Init options (proxy/dind/tmux) default sensibly; the UI can override.
// Returns { id, workspace } for the new project.
func (d *dashboardServer) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Mode   string `json:"mode"`
		Path   string `json:"path"`
		Name   string `json:"name"`
		RepoID string `json:"repoId"`
		Branch string `json:"branch"`
		// Init options; proxy defaults ON (the recommended/init default).
		Proxy *bool    `json:"proxy"`
		Dind  bool     `json:"dind"`
		Tmux  bool     `json:"tmux"`
		Ports []string `json:"ports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	workspace, err := d.resolveNewWorkspace(body.Mode, body.Path, body.Name, body.RepoID, body.Branch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Initialize the project config, unless "existing" already has one.
	proxy := true
	if body.Proxy != nil {
		proxy = *body.Proxy
	}
	if _, statErr := os.Stat(config.ProjectDirFor(workspace)); os.IsNotExist(statErr) {
		if _, err := project.InitProject(workspace, project.InitOptions{
			ProxyEnabled: proxy, DindEnabled: body.Dind, LaunchTmux: body.Tmux, DindPorts: body.Ports,
		}); err != nil {
			http.Error(w, "init project: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := RegisterProject(workspace); err != nil {
		http.Error(w, "register: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"id": ProjectID(workspace), "workspace": workspace})
}

// resolveNewWorkspace produces the workspace path for each create mode, doing
// the filesystem work (mkdir / clone) but not the project init.
func (d *dashboardServer) resolveNewWorkspace(mode, path, name, repoID, branch string) (string, error) {
	switch mode {
	case "existing":
		if strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("path is required for mode=existing")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if fi, serr := os.Stat(abs); serr != nil || !fi.IsDir() {
			return "", fmt.Errorf("not a directory: %s", abs)
		}
		return abs, nil

	case "new":
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("name is required for mode=new")
		}
		ws := uniqueWorkspacePath(name)
		if err := os.MkdirAll(ws, 0755); err != nil {
			return "", err
		}
		return ws, nil

	case "clone":
		if strings.TrimSpace(repoID) == "" {
			return "", fmt.Errorf("repoId is required for mode=clone")
		}
		repo, err := repos.Get(repoID)
		if err != nil {
			return "", err
		}
		label := name
		if label == "" {
			label = repo.Name
		}
		ws := uniqueWorkspacePath(label)
		if err := repos.CloneLocal(repoID, ws, branch); err != nil {
			return "", fmt.Errorf("clone: %w", err)
		}
		return ws, nil

	default:
		return "", fmt.Errorf("unknown mode: %q (want existing|new|clone)", mode)
	}
}
