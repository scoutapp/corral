package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/config"
)

// Live View preferred-port setting (#6 follow-up).
//
//	GET /p/<id>/live-port        → { port, path }   (0 = unset)
//	PUT /p/<id>/live-port  { port, path? }          (0 clears it)
//
// The host Claude sets this after it starts a web app so the Live View tab opens
// the user-facing port (e.g. a docs site on 1313) by default, instead of the user
// hunting for it. It's a stored per-project PREFERENCE — it grants no new reach
// (the reverse-proxy already serves any port on demand), and writing it goes
// through the same API-writes gate as any other change. The tab still lets the
// user pick a different port.
func (d *dashboardServer) handleLivePort(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		http.Error(w, "failed to read project config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"port": cfg.LiveViewPort, "path": cfg.LiveViewPath})
	case http.MethodPut:
		var body struct {
			Port int    `json:"port"`
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		// 0 clears the preference; otherwise require a valid TCP port.
		if body.Port != 0 && (body.Port < 1 || body.Port > 65535) {
			http.Error(w, "port must be 1-65535 (or 0 to clear)", http.StatusBadRequest)
			return
		}
		cfg.LiveViewPort = body.Port
		cfg.LiveViewPath = normalizeLivePath(body.Path)
		if err := config.WriteConfig(projectDirForWorkspace(workspace), cfg); err != nil {
			http.Error(w, "failed to save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "port": cfg.LiveViewPort, "path": cfg.LiveViewPath})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// normalizeLivePath cleans a stored Live View path: trims spaces, drops a bare
// "/" (that's just the root — store empty), and ensures a leading slash otherwise
// (so "docs/node/" and "/docs/node/" are equivalent). It does not force a
// trailing slash — the app decides.
func normalizeLivePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
