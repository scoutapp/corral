package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/mcp"
)

// MCP integrations — connect remote MCP servers at the HOST so the dashboard chat
// (and, later, flows) can use them. Corral drives the host `claude` CLI's native
// registry via internal/mcp; a server connected here is available to every host
// claude. The sandbox never reaches these — the config lives host-side and MCP
// hosts aren't in the sandbox allowlist.
//
// Routes (under /api/):
//   GET    /api/mcp          list servers + live status   (read — always allowed)
//   POST   /api/mcp          add a remote server           (write — gated)
//   DELETE /api/mcp/<name>   remove a server               (write — gated)
//
// The write gate (ApiWritesEnabled) is enforced in handleRoot before dispatch:
// it applies to the CLI / host Claude (API token), never to the browser (session
// token). So YOU can always manage servers in the Integrations tab; the gate only
// governs whether Claude may.
func (d *dashboardServer) handleMCP(w http.ResponseWriter, r *http.Request, name string) {
	client, err := d.mcpClient()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// /api/mcp/<name> — item routes (DELETE, and <name>/login).
	if name != "" {
		// <name>/login — start the interactive OAuth in a bridged terminal.
		if strings.HasSuffix(name, "/login") {
			srv, derr := url.PathUnescape(strings.TrimSuffix(name, "/login"))
			if derr != nil {
				http.Error(w, "bad server name", http.StatusBadRequest)
				return
			}
			d.handleMCPLogin(w, r, srv)
			return
		}
		decoded, derr := url.PathUnescape(name)
		if derr != nil {
			http.Error(w, "bad server name", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := client.Remove(r.Context(), decoded); err != nil {
				d.applog().ErrorfCtx(r.Context(), applog.CatSystem, "mcp.remove",
					applog.Fmt("Remove MCP server %q failed", decoded), err, map[string]any{"name": decoded})
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			d.applog().InfoCtx(r.Context(), applog.Entry{
				Category: applog.CatSystem, Event: "mcp.remove",
				Message: applog.Fmt("Removed MCP server %q", decoded),
				Status:  applog.StatusOK, Meta: map[string]any{"name": decoded},
			})
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// /api/mcp — collection routes.
	switch r.Method {
	case http.MethodGet:
		servers, err := client.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if servers == nil {
			servers = []mcp.Server{}
		}
		writeJSON(w, map[string]any{"servers": servers})

	case http.MethodPost:
		var body struct {
			Name      string `json:"name"`
			Transport string `json:"transport"`
			URL       string `json:"url"`
			Header    string `json:"header"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		spec := mcp.AddSpec{
			Name:      body.Name,
			Transport: mcp.Transport(body.Transport),
			URL:       body.URL,
			Header:    body.Header,
		}
		if err := client.Add(r.Context(), spec); err != nil {
			d.applog().ErrorfCtx(r.Context(), applog.CatSystem, "mcp.add",
				applog.Fmt("Add MCP server %q failed", body.Name), err, map[string]any{"name": body.Name, "url": body.URL})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		d.applog().InfoCtx(r.Context(), applog.Entry{
			Category: applog.CatSystem, Event: "mcp.add",
			Message: applog.Fmt("Connected MCP server %q", body.Name),
			Status:  applog.StatusOK, Meta: map[string]any{"name": body.Name, "url": body.URL, "transport": body.Transport},
		})
		writeJSON(w, map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// mcpLoginSession is the fixed tmux session the OAuth login runs in; the browser
// attaches to it over /api/mcp/login/ws (a bridged terminal).
const mcpLoginSession = "corral-mcp-login"

// handleMCPLogin starts `claude mcp login <name>` in a detached tmux session so
// its interactive OAuth (opens a browser, waits for the callback) can run, and
// returns the session id the Integrations tab attaches a terminal to. Mirrors
// handleGlobalPopulate (the claude setup-token flow). This is a mutating action,
// so it's gated for the CLI/Claude by handleRoot — but the browser (session
// token) is never gated, and in practice only the user drives this OAuth.
func (d *dashboardServer) handleMCPLogin(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if name == "" {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}
	bin, err := resolveClaudeBin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if err := d.spawnLoginSession(bin, name); err != nil {
		http.Error(w, "failed to start login session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	d.applog().InfoCtx(r.Context(), applog.Entry{
		Category: applog.CatSystem, Event: "mcp.login",
		Message: applog.Fmt("Started MCP login for %q", name),
		Meta:    map[string]any{"name": name},
	})
	writeJSON(w, map[string]any{"session": mcpLoginSession})
}

// spawnLoginSession starts `claude mcp login <name>` in the bridged-terminal tmux
// session. Split out (and overridable via loginSpawnerOverride) so tests exercise
// the endpoint without spawning a real tmux session. `; exec bash` keeps the pane
// open so the user can read the result; name is passed as a separate argv so a
// server name with spaces is safe.
func (d *dashboardServer) spawnLoginSession(bin, name string) error {
	if d.loginSpawnerOverride != nil {
		return d.loginSpawnerOverride(bin, name)
	}
	exec.Command("tmux", "kill-session", "-t", mcpLoginSession).Run()
	cmdline := fmt.Sprintf("%q mcp login %q; echo; echo '[done — you can close this]'; exec bash", bin, name)
	return exec.Command("tmux", "new-session", "-d", "-s", mcpLoginSession, "bash", "-lc", cmdline).Run()
}

// mcpClient builds an mcp.Client bound to the resolved host claude binary. Errors
// clearly when claude can't be located (same resolution the chat uses). Tests set
// mcpClientOverride to avoid shelling out to the real CLI.
func (d *dashboardServer) mcpClient() (*mcp.Client, error) {
	if d.mcpClientOverride != nil {
		return d.mcpClientOverride, nil
	}
	bin, err := resolveClaudeBin()
	if err != nil {
		return nil, err
	}
	return mcp.New(bin), nil
}
