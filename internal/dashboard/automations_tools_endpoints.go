package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/config"
)

// Tool adapter surface: exposes automations actions as callable tools for the
// host Claude, GATED behind a user-permission flag (GlobalSettings
// .AutomationsToolsEnabled, off by default). This is the bridge the eventual
// MCP/tool-server wraps — but the gate lives here so Claude can never act unless
// the user explicitly turned it on.
//
//	GET  /api/tools                 tool manifest (empty when disabled)
//	POST /api/tools/<name>:invoke   run the action behind a tool name
//
// The gate returns 403 (not 404) on invoke when disabled, so the caller sees a
// clear "not permitted" rather than a missing endpoint.

func toolsEnabled() bool {
	return config.ReadGlobalSettings().AutomationsToolsEnabled
}

func (d *dashboardServer) handleTools(w http.ResponseWriter, r *http.Request, rest string) {
	// "<name>:invoke"
	if name, ok := strings.CutSuffix(rest, ":invoke"); ok && name != "" {
		d.handleToolInvoke(w, r, name)
		return
	}
	if rest != "" {
		http.NotFound(w, r)
		return
	}
	// GET /api/tools — manifest.
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// When disabled, advertise nothing (an empty toolset) rather than erroring —
	// a tool client polling the manifest simply sees no corral tools.
	if !toolsEnabled() {
		writeJSON(w, map[string]any{"enabled": false, "tools": []any{}})
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	tools, err := svc.ToolManifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"enabled": true, "tools": tools})
}

func (d *dashboardServer) handleToolInvoke(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !toolsEnabled() {
		http.Error(w, "automations tools are disabled — enable them in global settings to let Claude invoke them", http.StatusForbidden)
		return
	}
	svc := d.automationsService(w)
	if svc == nil {
		return
	}
	a, err := svc.ActionForTool(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// The tool input IS the run-context vars.
	var vars map[string]string
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&vars)
	}
	runner := d.automationsRunner(svc)
	res, err := runner.RunAction(r.Context(), a.ID, automations.TriggerAPI, automations.RunContext{Vars: vars})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}
