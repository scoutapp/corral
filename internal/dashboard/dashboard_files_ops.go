package dashboard

import (
	"net/http"
	"os"
	"path/filepath"
)

// Mutating filesystem operations for the Files tab: create file, mkdir, rename,
// delete. All are POST/DELETE, safeJoin-guarded (no "../" escape), and refuse to
// touch the workspace root itself. The workspace is bind-mounted into the
// container, so these edit the project's files directly on the host.

// handleFilesNew creates a new empty file. Errors if it already exists.
// POST /p/<id>/files/new?path=<workspace-relative file>
func (d *dashboardServer) handleFilesNew(w http.ResponseWriter, r *http.Request, id string) {
	abs, ok := d.resolveMut(w, r, id, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if _, err := os.Stat(abs); err == nil {
		http.Error(w, "already exists", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.Close()
	writeFilesJSON(w, map[string]any{"ok": true})
}

// handleFilesMkdir creates a directory (and parents). Idempotent.
// POST /p/<id>/files/mkdir?path=<workspace-relative dir>
func (d *dashboardServer) handleFilesMkdir(w http.ResponseWriter, r *http.Request, id string) {
	abs, ok := d.resolveMut(w, r, id, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// handleFilesRename moves/renames a path. Both ends are safeJoin-guarded.
// POST /p/<id>/files/rename?from=<rel>&to=<rel>
func (d *dashboardServer) handleFilesRename(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	from, okF := safeJoinMut(workspace, r.URL.Query().Get("from"))
	to, okT := safeJoinMut(workspace, r.URL.Query().Get("to"))
	if !okF || !okT {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if _, serr := os.Stat(to); serr == nil {
		http.Error(w, "destination already exists", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(from, to); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// handleFilesDelete removes a file or directory (recursively for dirs).
// DELETE /p/<id>/files?path=<workspace-relative>
func (d *dashboardServer) handleFilesDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	abs, ok := d.resolveMutMethod(w, r, id, r.URL.Query().Get("path"), true)
	if !ok {
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// resolveMut looks up the workspace, requires POST, and safeJoins the rel path
// (refusing the workspace root). Returns the absolute path or writes an error.
func (d *dashboardServer) resolveMut(w http.ResponseWriter, r *http.Request, id, rel string) (string, bool) {
	return d.resolveMutMethod(w, r, id, rel, false)
}

func (d *dashboardServer) resolveMutMethod(w http.ResponseWriter, r *http.Request, id, rel string, isDelete bool) (string, bool) {
	wantMethod := http.MethodPost
	if isDelete {
		wantMethod = http.MethodDelete
	}
	if r.Method != wantMethod {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return "", false
	}
	abs, ok := safeJoinMut(workspace, rel)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return "", false
	}
	return abs, true
}

// safeJoinMut is safeJoin plus a guard that the resolved path is NOT the
// workspace root itself (you can't rename/delete/create-over the whole workspace).
func safeJoinMut(workspace, rel string) (string, bool) {
	abs, ok := safeJoin(workspace, rel)
	if !ok || abs == workspace {
		return "", false
	}
	return abs, true
}
