package dashboard

import (
	"encoding/json"
	"net/http"
	"net/url"

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

	// /api/mcp/<name> — item routes (DELETE).
	if name != "" {
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
