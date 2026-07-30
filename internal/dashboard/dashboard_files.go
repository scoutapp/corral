package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Files & git tab handlers. The project workspace is bind-mounted into the
// container at the same absolute path, so the dashboard reads/writes/diffs the
// project's files DIRECTLY on the host — no docker needed, and a host write is
// immediately visible inside the container.

// maxEditableBytes caps how large a file we send to the browser editor. Bigger
// files are almost always build output / binaries, not things to hand-edit.
const maxEditableBytes = 2 << 20 // 2 MiB

// dirsToSkip are never listed in the file tree (noise / not project source).
var dirsToSkip = map[string]bool{
	".git":         true,
	".sandclaude":  true,
	"node_modules": true,
}

// safeJoin resolves rel against workspace and guarantees the result stays inside
// workspace, defeating "../" traversal and absolute-path escapes. An empty rel
// means the workspace root. Returns (absPath, ok).
func safeJoin(workspace, rel string) (string, bool) {
	// Treat the input as workspace-relative regardless of leading slashes.
	clean := filepath.Clean("/" + strings.TrimPrefix(rel, "/")) // e.g. "/a/../b" -> "/b"
	abs := filepath.Join(workspace, clean)
	// abs must equal workspace or be under workspace + separator.
	if abs != workspace && !strings.HasPrefix(abs, workspace+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

type fileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// handleFilesTree lists a single directory level (lazy tree expansion).
// GET /p/<id>/files/tree?path=<workspace-relative dir>
func (d *dashboardServer) handleFilesTree(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dir, ok := safeJoin(workspace, r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && dirsToSkip[e.Name()] {
			continue
		}
		var size int64
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		out = append(out, fileEntry{Name: e.Name(), Dir: e.IsDir(), Size: size})
	}
	// Dirs first, then files, each alphabetical — the conventional tree order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	writeFilesJSON(w, map[string]any{"entries": out})
}

// handleFilesRead returns a file's contents for the editor.
// GET /p/<id>/files/read?path=<workspace-relative file>
func (d *dashboardServer) handleFilesRead(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, ok := safeJoin(workspace, r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxEditableBytes {
		writeFilesJSON(w, map[string]any{
			"too_large": true,
			"size":      info.Size(),
		})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{
		"content":  string(data),
		"filename": filepath.Base(path),
	})
}

// handleFilesWrite saves editor contents back to a file. The raw request body is
// the new file contents. POST /p/<id>/files/write?path=<workspace-relative file>
func (d *dashboardServer) handleFilesWrite(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, ok := safeJoin(workspace, r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	// Don't allow creating/overwriting a directory as a file.
	if info, serr := os.Stat(path); serr == nil && info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	body, err := readAllLimited(r, maxEditableBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// ----------------------------------------------------------------------------
// Search: filename find + content grep
// ----------------------------------------------------------------------------

const maxSearchResults = 200

// handleFilesFind returns workspace-relative paths whose name matches q
// (case-insensitive substring). GET /p/<id>/files/find?q=<query>
func (d *dashboardServer) handleFilesFind(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeFilesJSON(w, map[string]any{"matches": []string{}})
		return
	}
	matches := make([]string, 0, 64)
	truncated := false
	filepath.WalkDir(workspace, func(path string, dirent os.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}
		name := dirent.Name()
		if dirent.IsDir() {
			if dirsToSkip[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(matches) >= maxSearchResults {
			truncated = true
			return filepath.SkipAll
		}
		if strings.Contains(strings.ToLower(name), q) {
			if rel, rerr := filepath.Rel(workspace, path); rerr == nil {
				matches = append(matches, rel)
			}
		}
		return nil
	})
	writeFilesJSON(w, map[string]any{"matches": matches, "truncated": truncated})
}

type grepHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// handleFilesGrep searches file CONTENTS for q across the workspace, preferring
// `git grep` (fast, respects .gitignore) with a `grep -rn` fallback for non-repos.
// GET /p/<id>/files/grep?q=<query>
func (d *dashboardServer) handleFilesGrep(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeFilesJSON(w, map[string]any{"hits": []grepHit{}})
		return
	}

	var out string
	isRepo := false
	if s, gerr := gitCmd(workspace, "rev-parse", "--is-inside-work-tree"); gerr == nil && strings.TrimSpace(s) == "true" {
		isRepo = true
	}
	if isRepo {
		// -I skip binary, -n line numbers, -F fixed-string (literal), -i case-insensitive.
		out, _ = gitCmd(workspace, "grep", "-I", "-n", "-F", "-i", "-e", q)
	} else {
		cmd := exec.Command("grep", "-rInF", "-i", "--", q, ".")
		cmd.Dir = workspace
		b, _ := cmd.Output() // grep exits 1 on no matches; ignore, parse what we got
		out = string(b)
	}

	hits := make([]grepHit, 0, 64)
	truncated := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if len(hits) >= maxSearchResults {
			truncated = true
			break
		}
		// Format: path:line:text  (grep -rn prepends "./" — trim it)
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		ln, _ := strconv.Atoi(parts[1])
		p := strings.TrimPrefix(parts[0], "./")
		text := parts[2]
		if len(text) > 300 {
			text = text[:300] + "…"
		}
		hits = append(hits, grepHit{Path: p, Line: ln, Text: text})
	}
	writeFilesJSON(w, map[string]any{"hits": hits, "truncated": truncated})
}

// ----------------------------------------------------------------------------
// Git diff
// ----------------------------------------------------------------------------

type gitChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // porcelain XY, e.g. " M", "??", "A "
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// handleGitStatus reports the changed files for the workspace. Default mode is
// the working tree vs HEAD. With valid base & target ref query params it instead
// reports the changes between those two refs (base..target).
// GET /p/<id>/git/status[?repo=<subdir>&base=<ref>&target=<ref>]
func (d *dashboardServer) handleGitStatus(w http.ResponseWriter, r *http.Request, id string) {
	ws, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	workspace, ok := gitRepoDir(ws, r)
	if !ok {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	// Is this a git repo at all?
	if out, gerr := gitCmd(workspace, "rev-parse", "--is-inside-work-tree"); gerr != nil || strings.TrimSpace(out) != "true" {
		writeFilesJSON(w, map[string]any{"repo": false})
		return
	}

	base := r.URL.Query().Get("base")
	target := r.URL.Query().Get("target")
	if base != "" || target != "" {
		d.gitStatusRefs(w, workspace, base, target)
		return
	}

	status, err := gitCmd(workspace, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// numstat over tracked changes (added/removed line counts) — key by path.
	nums := map[string][2]int{}
	if numstat, nerr := gitCmd(workspace, "diff", "--numstat", "HEAD"); nerr == nil {
		for _, line := range strings.Split(numstat, "\n") {
			f := strings.Fields(line)
			if len(f) < 3 {
				continue
			}
			a, _ := strconv.Atoi(f[0]) // "-" for binary -> 0, fine
			rm, _ := strconv.Atoi(f[1])
			nums[f[2]] = [2]int{a, rm}
		}
	}

	changes := make([]gitChange, 0)
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		p := strings.TrimSpace(line[3:])
		// Renames show "old -> new"; take the new path.
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.Trim(p, `"`)
		n := nums[p]
		changes = append(changes, gitChange{Path: p, Status: xy, Added: n[0], Removed: n[1]})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	writeFilesJSON(w, map[string]any{"repo": true, "mode": "worktree", "changes": changes})
}

// gitStatusRefs reports the changed files between two refs (base..target). Both
// must validate; base defaults to HEAD if only target is given (and vice versa).
func (d *dashboardServer) gitStatusRefs(w http.ResponseWriter, workspace, base, target string) {
	if base == "" {
		base = "HEAD"
	}
	if target == "" {
		target = "HEAD"
	}
	if !validRef(workspace, base) || !validRef(workspace, target) {
		http.Error(w, "unknown git ref", http.StatusBadRequest)
		return
	}
	rangeArg := base + ".." + target

	// name-status for the change kind (A/M/D/R), numstat for line counts.
	nums := map[string][2]int{}
	if numstat, nerr := gitCmd(workspace, "diff", "--numstat", rangeArg); nerr == nil {
		for _, line := range strings.Split(numstat, "\n") {
			f := strings.Fields(line)
			if len(f) < 3 {
				continue
			}
			a, _ := strconv.Atoi(f[0])
			rm, _ := strconv.Atoi(f[1])
			nums[f[2]] = [2]int{a, rm}
		}
	}
	nameStatus, err := gitCmd(workspace, "diff", "--name-status", rangeArg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	changes := make([]gitChange, 0)
	for _, line := range strings.Split(nameStatus, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) < 2 {
			continue
		}
		code := f[0]
		p := f[len(f)-1] // rename rows are "R100\told\tnew" — take the new path
		n := nums[p]
		// Present the kind as a porcelain-ish XY the frontend's statusLabel reads.
		xy := " " + string(code[0])
		changes = append(changes, gitChange{Path: p, Status: xy, Added: n[0], Removed: n[1]})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	writeFilesJSON(w, map[string]any{"repo": true, "mode": "refs", "base": base, "target": target, "changes": changes})
}

// handleGitDiff returns the unified diff for one path (working tree vs HEAD).
// The path is relative to the selected repo (see gitRepoDir).
// GET /p/<id>/git/diff?path=<repo-relative file>[&repo=<subdir>&base=&target=]
func (d *dashboardServer) handleGitDiff(w http.ResponseWriter, r *http.Request, id string) {
	ws, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	workspace, ok := gitRepoDir(ws, r)
	if !ok {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	rel := r.URL.Query().Get("path")
	if _, ok := safeJoin(workspace, rel); !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Ref-diff mode: with valid base/target, show that file's diff across the two
	// refs. Otherwise fall through to the working-tree-vs-HEAD default.
	base := r.URL.Query().Get("base")
	target := r.URL.Query().Get("target")
	if base != "" || target != "" {
		if base == "" {
			base = "HEAD"
		}
		if target == "" {
			target = "HEAD"
		}
		if !validRef(workspace, base) || !validRef(workspace, target) {
			http.Error(w, "unknown git ref", http.StatusBadRequest)
			return
		}
		diff, derr := gitCmd(workspace, "diff", base+".."+target, "--", rel)
		if derr != nil {
			http.Error(w, derr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, diff)
		return
	}

	// `git diff HEAD -- <path>` shows staged+unstaged changes vs the last commit.
	// For an untracked file (no HEAD blob) git diff prints nothing, so fall back
	// to diffing against /dev/null to show the whole file as added.
	diff, err := gitCmd(workspace, "diff", "HEAD", "--", rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(diff) == "" {
		if untracked, uerr := gitCmd(workspace, "diff", "--no-index", "--", os.DevNull, rel); uerr == nil {
			diff = untracked
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, diff)
}

// gitCmd runs `git <args...>` in the workspace dir and returns combined stdout.
// git's own non-zero exits for "no diff" (git diff --no-index) are tolerated by
// callers; a genuine failure returns the error with stderr attached.
func gitCmd(workspace string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.Output()
	if err != nil {
		// git diff --no-index exits 1 when files differ — that's not an error for us.
		if ee, ok := err.(*exec.ExitError); ok && len(out) > 0 {
			_ = ee
			return string(out), nil
		}
		return "", err
	}
	return string(out), nil
}

// writeFilesJSON is the small JSON responder shared by the files handlers.
func writeFilesJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// validRef confirms a ref string names a real git object in the workspace before
// we splice it into a git command. This is the injection guard: refs come from
// the client, so we never pass an unverified string to git. Empty is allowed by
// callers that treat it as "working tree" / default.
func validRef(workspace, ref string) bool {
	if ref == "" {
		return false
	}
	// rev-parse --verify resolves branches, tags, and SHAs; ^{commit} ensures it's
	// a commit-ish. Reject anything that doesn't resolve.
	if _, err := gitCmd(workspace, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return false
	}
	return true
}

// gitRepoDir resolves the git working dir for a request. A workspace can hold
// more than one repo — submodules, or a parent dir whose children are each their
// own repo — so a `repo` query param (workspace-relative, from /git/repos)
// selects which one. Empty means the workspace root. safeJoin guards against
// escaping the workspace. Returns (absDir, ok).
func gitRepoDir(workspace string, r *http.Request) (string, bool) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		return workspace, true
	}
	return safeJoin(workspace, repo)
}

// gitRepo is one repo detected within a workspace. Path is workspace-relative
// ("" for the root); Name is a friendly label for the picker.
type gitRepo struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// maxRepoScanDepth bounds how deep we look for nested repos, keeping the scan
// fast on large trees. Depth 0 = workspace root, so 2 covers workspace/*/ and
// workspace/*/*/ — enough for sibling-projects and typical submodule layouts.
const maxRepoScanDepth = 2

// handleGitRepos lists the git repositories inside the workspace so the UI can
// offer a repo picker. Shallow-scans for directories containing a .git entry
// (dir OR file — submodules use a .git file). GET /p/<id>/git/repos
func (d *dashboardServer) handleGitRepos(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	repos := make([]gitRepo, 0)
	rootIsRepo := false
	if _, serr := os.Stat(filepath.Join(workspace, ".git")); serr == nil {
		rootIsRepo = true
		repos = append(repos, gitRepo{Path: "", Name: filepath.Base(workspace) + " (root)"})
	}

	var scan func(dir string, depth int)
	scan = func(dir string, depth int) {
		if depth > maxRepoScanDepth {
			return
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || dirsToSkip[e.Name()] {
				continue
			}
			child := filepath.Join(dir, e.Name())
			if _, gerr := os.Stat(filepath.Join(child, ".git")); gerr == nil {
				if rel, relErr := filepath.Rel(workspace, child); relErr == nil {
					repos = append(repos, gitRepo{Path: rel, Name: rel})
				}
				continue // don't descend into a repo (its submodules are its own concern)
			}
			scan(child, depth+1)
		}
	}
	scan(workspace, 1)

	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	writeFilesJSON(w, map[string]any{"rootIsRepo": rootIsRepo, "repos": repos})
}

// handleGitRefs lists the refs available for the diff picker: local branches,
// the current HEAD, and tags. GET /p/<id>/git/refs[?repo=<subdir>]
func (d *dashboardServer) handleGitRefs(w http.ResponseWriter, r *http.Request, id string) {
	ws, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	workspace, ok := gitRepoDir(ws, r)
	if !ok {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	if out, gerr := gitCmd(workspace, "rev-parse", "--is-inside-work-tree"); gerr != nil || strings.TrimSpace(out) != "true" {
		writeFilesJSON(w, map[string]any{"repo": false})
		return
	}
	current := ""
	if c, cerr := gitCmd(workspace, "rev-parse", "--abbrev-ref", "HEAD"); cerr == nil {
		current = strings.TrimSpace(c)
	}
	branches := gitRefList(workspace, "refs/heads")
	tags := gitRefList(workspace, "refs/tags")
	writeFilesJSON(w, map[string]any{
		"repo": true, "current": current, "branches": branches, "tags": tags,
	})
}

// gitRefList returns the short names under a ref namespace (e.g. refs/heads).
func gitRefList(workspace, namespace string) []string {
	out, err := gitCmd(workspace, "for-each-ref", "--format=%(refname:short)", "--sort=-committerdate", namespace)
	names := make([]string, 0)
	if err != nil {
		return names
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names
}

// readAllLimited reads the request body, erroring if it exceeds max bytes (so a
// runaway upload can't exhaust memory). io.LimitReader caps at max+1 so we can
// distinguish "exactly max" from "over".
func readAllLimited(r *http.Request, max int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("file too large (limit %d bytes)", max)
	}
	return buf, nil
}
