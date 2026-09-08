package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// renderedTextThreshold is the minimum rendered visible-text length we treat as
// "the page actually shows something". A blank/white-screen SPA renders an empty
// root → near-zero; a real page (even a sparse one) clears this comfortably.
const renderedTextThreshold = 40

// POST /p/<id>/verify-live-view { port?, path? } — render-check the Live View page
// the way a BROWSER sees it, and report whether it actually renders (vs a white
// screen). This is the only place that can: the live proxy is a host loopback
// listener gated by the live token, and the sandbox can't reach the dashboard —
// so the dashboard drives a host headless browser at the real live URL
// (localhost:<livePort>/p/<id>/live/<port><path>?__live_token=…), runs its JS, and
// measures the rendered DOM. curl can't do this (it never runs JS, and gets 403
// without the live cookie).
//
// port/path default to the project's saved live-port preference; the body can
// override them (to check before committing a port). Reports only — it never
// changes the saved live-port. Returns { rendered, url, domTextLen, browser,
// screenshot?, note? }.
func (d *dashboardServer) handleVerifyLiveView(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

	var body struct {
		Port int    `json:"port"`
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional

	port := body.Port
	if port == 0 {
		port = cfg.LiveViewPort
	}
	path := body.Path
	if path == "" {
		path = cfg.LiveViewPath
	}
	if port == 0 {
		http.Error(w, "no live-view port set for this project (pass {\"port\":…} or PUT /p/<id>/live-port first)", http.StatusBadRequest)
		return
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// The exact URL the browser iframe loads (see LiveViewTab.tsx): the localhost
	// live origin, the /p/<id>/live/<port><path> proxy route, and the one-time
	// ?__live_token= that the live listener swaps for a cookie and strips.
	url := fmt.Sprintf("http://localhost:%d/p/%s/live/%d%s?%s=%s",
		d.livePort, id, port, path, liveTokenParam, d.liveToken)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rr := d.headlessRender(ctx, url)

	rendered := rr.BrowserFound && rr.DOMTextLen >= renderedTextThreshold
	resp := map[string]any{
		"rendered":   rendered,
		"url":        strings.Replace(url, d.liveToken, "***", 1), // don't echo the token
		"port":       port,
		"path":       path,
		"domTextLen": rr.DOMTextLen,
		"browser":    rr.Browser,
	}
	if rr.Screenshot != "" {
		resp["screenshot"] = rr.Screenshot
	}
	if !rr.BrowserFound {
		resp["note"] = rr.Err // no host browser: can't verify — the caller must eyeball it
	} else if !rendered {
		resp["note"] = fmt.Sprintf("the page rendered almost no content (%d chars) — likely a white screen (JS error, empty root, or wrong route). Fix the app and re-verify; do NOT publish this as working.", rr.DOMTextLen)
	} else if rr.Err != "" {
		resp["note"] = rr.Err // e.g. screenshot failed but DOM was fine
	}
	writeJSON(w, resp)
}
