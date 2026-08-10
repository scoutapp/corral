package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

// expandWorkspaceParent resolves a user-supplied parent directory for a blank
// project: "~"/"~/x" → under home, absolute → as-is, anything else (bare or
// relative) → under the home dir (so "code/foo" lands in ~/code/foo, not some
// server cwd the user can't see). Existence is checked by the caller.
func expandWorkspaceParent(p string) string {
	p = strings.TrimSpace(p)
	home, _ := os.UserHomeDir()
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	default:
		return filepath.Join(home, p)
	}
}

// issueSeed carries a GitHub issue to seed a spawned project with. The frontend
// fills it from `gh issue list`; the backend uses it to create an issue branch,
// write ISSUE.md, and build the pre-populate prompt.
type issueSeed struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Repo   string `json:"repo"` // owner/name, for the ISSUE.md header
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
		// Issue: when spawning a project off a GitHub issue, seed the workspace
		// with a branch + ISSUE.md, and record a prompt to pre-populate into Claude.
		Issue *issueSeed `json:"issue"`
		// Init options; proxy defaults ON (the recommended/init default).
		Proxy       *bool    `json:"proxy"`
		Passthrough bool     `json:"passthrough"` // permissive-but-observed firewall
		Dind        bool     `json:"dind"`
		Tmux        bool     `json:"tmux"`
		Ports       []string `json:"ports"`
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

	// Issue seeding: create an issue branch in the cloned repo + write ISSUE.md at
	// the workspace root. Best-effort — a seeding hiccup shouldn't fail the whole
	// create (the project is already cloned). Returns the prompt to pre-populate.
	var issuePrompt string
	if body.Issue != nil && body.Issue.Number > 0 {
		// A fresh project inherits the global SSH keys (union model). If any are
		// configured, the prompt tells Claude to push over the SSH remote (the
		// HTTPS remote won't auth in the sandbox).
		hasSSHKey := len(config.GlobalSSHKeys()) > 0
		issuePrompt = seedIssue(workspace, body.Repos, body.Issue, hasSSHKey)
	}

	// Initialize the project config, unless "existing" already has one.
	proxy := true
	if body.Proxy != nil {
		proxy = *body.Proxy
	}
	if _, statErr := os.Stat(config.ProjectDirFor(workspace)); os.IsNotExist(statErr) {
		if _, err := project.InitProject(workspace, project.InitOptions{
			ProxyEnabled: proxy, DindEnabled: body.Dind, LaunchTmux: body.Tmux, DindPorts: body.Ports,
			PassthroughFirewall: body.Passthrough && proxy, // only meaningful with the proxy on
		}); err != nil {
			http.Error(w, "init project: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := RegisterProject(workspace); err != nil {
		http.Error(w, "register: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Pre-trust the workspace in ~/.claude.json so the container's Claude (which
	// mounts the host ~/.claude.json) skips the "Do you trust the files in this
	// folder?" dialog on first launch. Without this, every freshly-created project
	// blocks on that dialog — and anything typed (e.g. an issue prompt) lands in
	// the dialog, not Claude's input. Best-effort: a hiccup here shouldn't fail the
	// create; Claude just shows its normal trust prompt.
	if err := trustWorkspaceInClaudeConfig(workspace); err != nil {
		// non-fatal; log-only via debug (no logger here — swallow)
		_ = err
	}
	writeFilesJSON(w, map[string]any{
		"id": ProjectID(workspace), "workspace": workspace,
		"issue_prompt": issuePrompt, // "" unless spawned from an issue
	})
}

// trustWorkspaceInClaudeConfig marks a workspace path as trusted in the host's
// ~/.claude.json (projects.<path>.hasTrustDialogAccepted = true), so Claude
// doesn't show its workspace-trust dialog for it. The container mounts this same
// file, and runs Claude at the identical absolute workspace path, so the key
// matches. Preserves every other field; creates the file/structure if absent.
func trustWorkspaceInClaudeConfig(workspace string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude.json")

	var root map[string]any
	if data, rerr := os.ReadFile(path); rerr == nil {
		if jerr := json.Unmarshal(data, &root); jerr != nil {
			// Don't clobber a file we can't parse — bail rather than risk corruption.
			return jerr
		}
	}
	if root == nil {
		root = map[string]any{}
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		root["projects"] = projects
	}
	entry, _ := projects[workspace].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[workspace] = entry
	}
	entry["hasTrustDialogAccepted"] = true

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	// 0600: ~/.claude.json holds the user's Claude config; keep it user-private.
	return os.WriteFile(path, out, 0600)
}

// issueBranchSlug builds a git-branch-safe "issue-<n>-<title-slug>" name.
func issueBranchSlug(number int, title string) string {
	slug := strings.ToLower(wsSlugRe.ReplaceAllString(title, "-"))
	slug = strings.Trim(slug, "-.")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-.")
	}
	if slug == "" {
		return fmt.Sprintf("issue-%d", number)
	}
	return fmt.Sprintf("issue-%d-%s", number, slug)
}

// seedIssue prepares a spawned project to work on a GitHub issue:
//   - create + checkout an `issue-<n>-<slug>` branch in the (single) cloned repo,
//   - write ISSUE.md at the workspace root with the full issue context,
//
// and returns a prompt to pre-populate into Claude. All best-effort: the clone
// already succeeded, so a failed branch/file step is logged (via the returned
// prompt still being useful) but never fails the create.
func seedIssue(workspace string, specs []repoSpec, iss *issueSeed, hasSSHKey bool) string {
	// The repo landed in a subdir of the workspace (see cloneMultiRepoWorkspace).
	// Seed the first repo (issue-spawn is single-repo in the UI).
	repoDir := workspace
	if len(specs) >= 1 {
		dir := specs[0].Dir
		if dir == "" {
			dir = specDirName(specs[0])
		}
		repoDir = filepath.Join(workspace, dir)
	}

	branch := issueBranchSlug(iss.Number, iss.Title)
	// `git checkout -b <branch>` in the repo dir; ignore errors (e.g. non-git dir).
	if fi, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && fi.IsDir() {
		_ = exec.Command("git", "-C", repoDir, "checkout", "-b", branch).Run()
	}

	// ISSUE.md at the workspace root (visible to Claude, above the repo subdir).
	md := fmt.Sprintf("# %s #%d: %s\n\n%s\n\n---\nIssue: %s\nBranch: `%s`\n",
		iss.Repo, iss.Number, iss.Title, strings.TrimSpace(iss.Body), iss.URL, branch)
	_ = os.WriteFile(filepath.Join(workspace, "ISSUE.md"), []byte(md), 0644)

	prompt := fmt.Sprintf("Work on %s issue #%d: %s. The full description is in ISSUE.md at the workspace root. You're on branch %s.",
		iss.Repo, iss.Number, iss.Title, branch)
	// When an SSH key is set up for this project, push over the SSH remote — the
	// HTTPS remote won't authenticate in the sandbox (no token), but the scoped
	// ssh-agent holds the key. github owner/name is iss.Repo.
	if hasSSHKey && iss.Repo != "" {
		prompt += fmt.Sprintf(" When you push, use the SSH remote (git@github.com:%s.git) — the scoped ssh-agent has the key; the HTTPS remote won't authenticate here.", iss.Repo)
	}
	return prompt
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
		// Optional location: create <parent>/<name> under a user-chosen parent dir
		// (~, an absolute path, or a path relative to the home dir). Absent → the
		// managed ~/.sandclaude/workspaces dir. This lets a blank project live in
		// the operator's own tree instead of being buried in the managed dir.
		var ws string
		if p := strings.TrimSpace(path); p != "" {
			parent := expandWorkspaceParent(p)
			if fi, serr := os.Stat(parent); serr != nil || !fi.IsDir() {
				return "", fmt.Errorf("parent directory does not exist: %s", parent)
			}
			ws = filepath.Join(parent, name)
			if _, serr := os.Stat(ws); serr == nil {
				return "", fmt.Errorf("already exists: %s", ws)
			}
		} else {
			ws = uniqueWorkspacePath(name)
		}
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
