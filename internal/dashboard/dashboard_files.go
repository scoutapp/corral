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
	".git":        true,
	".sandclaude": true,
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
// Git diff
// ----------------------------------------------------------------------------

type gitChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // porcelain XY, e.g. " M", "??", "A "
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// handleGitStatus reports the working-tree changes for the workspace.
// GET /p/<id>/git/status
func (d *dashboardServer) handleGitStatus(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Is this a git repo at all?
	if out, gerr := gitCmd(workspace, "rev-parse", "--is-inside-work-tree"); gerr != nil || strings.TrimSpace(out) != "true" {
		writeFilesJSON(w, map[string]any{"repo": false})
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
	writeFilesJSON(w, map[string]any{"repo": true, "changes": changes})
}

// handleGitDiff returns the unified diff for one path (working tree vs HEAD).
// GET /p/<id>/git/diff?path=<workspace-relative file>
func (d *dashboardServer) handleGitDiff(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rel := r.URL.Query().Get("path")
	if _, ok := safeJoin(workspace, rel); !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
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
