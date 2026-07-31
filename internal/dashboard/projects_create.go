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

// repoSpec is one repo to clone into a multi-repo project. Exactly one source
// (RepoID for a listed repo, or URL / LocalPath for an ad-hoc one) is set.
type repoSpec struct {
	RepoID    string `json:"repoId"`
	URL       string `json:"url"`
	LocalPath string `json:"localPath"`
	Branch    string `json:"branch"`
	Dir       string `json:"dir"` // optional subdir name override
}

// handleCreateProject creates a project from the dashboard and registers it.
//
//	POST /projects/create
//	{ "mode": "existing"|"new"|"clone", "name?", ...mode-specific, ...init opts }
//
// existing: { path }                    — register an existing dir in place.
// new:      { name }                    — a fresh empty managed workspace (blank project).
// clone:    { name?, repos: [ {repoId|url|localPath, branch?, dir?}, ... ] }
//
//	— clone each repo into a SUBDIR of one parent workspace; the container
//	runs at the parent. A single repo still works (one-element list, or the
//	legacy single repoId field).
//
// Init options (proxy/dind/tmux) default sensibly; the UI can override.
// Returns { id, workspace }.
func (d *dashboardServer) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Mode   string     `json:"mode"`
		Path   string     `json:"path"`
		Name   string     `json:"name"`
		RepoID string     `json:"repoId"` // legacy single-repo shorthand
		Branch string     `json:"branch"`
		Repos  []repoSpec `json:"repos"`
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
	// Fold the legacy single repoId into the repos list.
	if body.RepoID != "" && len(body.Repos) == 0 {
		body.Repos = []repoSpec{{RepoID: body.RepoID, Branch: body.Branch}}
	}

	workspace, err := d.resolveNewWorkspace(body.Mode, body.Path, body.Name, body.Repos)
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
func (d *dashboardServer) resolveNewWorkspace(mode, path, name string, specs []repoSpec) (string, error) {
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
		if len(specs) == 0 {
			return "", fmt.Errorf("at least one repo is required for mode=clone")
		}
		return d.cloneMultiRepoWorkspace(name, specs)

	default:
		return "", fmt.Errorf("unknown mode: %q (want existing|new|clone)", mode)
	}
}

// cloneMultiRepoWorkspace creates one parent workspace and clones each spec into
// a subdirectory of it (the container runs at the parent). The project name
// defaults to the sole repo's name for a single-repo project, else "workspace".
// On any clone failure the whole parent is removed so no half-built project is
// registered.
func (d *dashboardServer) cloneMultiRepoWorkspace(name string, specs []repoSpec) (string, error) {
	label := strings.TrimSpace(name)
	if label == "" {
		if len(specs) == 1 {
			label = specDirName(specs[0])
		} else {
			label = "workspace"
		}
	}
	parent := uniqueWorkspacePath(label)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", err
	}

	usedDirs := map[string]bool{}
	for i, s := range specs {
		dir := s.Dir
		if dir == "" {
			dir = specDirName(s)
		}
		// De-dup subdir names within one workspace.
		if usedDirs[dir] {
			dir = fmt.Sprintf("%s-%d", dir, i+1)
		}
		usedDirs[dir] = true
		dest := filepath.Join(parent, dir)

		var err error
		switch {
		case s.RepoID != "":
			err = repos.CloneLocal(s.RepoID, dest, s.Branch)
		case s.URL != "":
			err = repos.CloneURLInto(s.URL, dest, s.Branch)
		case s.LocalPath != "":
			err = repos.CloneURLInto(s.LocalPath, dest, s.Branch)
		default:
			err = fmt.Errorf("repo #%d has no source (repoId/url/localPath)", i+1)
		}
		if err != nil {
			os.RemoveAll(parent)
			return "", fmt.Errorf("clone %s: %w", dir, err)
		}
	}
	return parent, nil
}

// specDirName picks a subdir name for a repo spec from its source.
func specDirName(s repoSpec) string {
	switch {
	case s.RepoID != "":
		if repo, err := repos.Get(s.RepoID); err == nil {
			return strings.TrimSuffix(repo.Name, ".git")
		}
		return s.RepoID
	case s.URL != "":
		return repos.DefaultDirName(s.URL)
	case s.LocalPath != "":
		return repos.DefaultDirName(s.LocalPath)
	}
	return "repo"
}
