package dashboard

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// The Live View SECOND listener. It serves the reverse-proxied sandbox app on a
// DISTINCT ORIGIN from the main dashboard: the dashboard is http://127.0.0.1:<port>,
// this listener is reached as http://localhost:<livePort> (a different host string
// → a different browser origin AND a different host-only cookie jar).
//
// Why this exists: the framed app is UNTRUSTED and the iframe uses
// allow-same-origin (so the app's cookies/login/CSS work). If the app were served
// from the DASHBOARD's origin, allow-same-origin would let its page JS call the
// dashboard's APIs as the user (same-origin fetch + the auto-attached HttpOnly
// dashboard cookie) — a sandbox→host escalation. Serving it from localhost:<livePort>
// puts the app on its OWN origin: its JS can reach that origin only, and the
// dashboard's corral_dash_token (host-only to 127.0.0.1) is never sent here.
//
// This mux serves ONLY the live proxy + a health probe. It is NOT the dashboard:
// no SPA, no /api, no terminal, no /status. Its own token (liveToken, via the
// corral_live_token cookie) gates it; the dashboard token is never accepted.

// liveCookieName is the cookie the live listener sets/reads on the localhost
// origin. Distinct from dashboardCookieName so the two origins' credentials never
// mix, and host-only (no Domain) so it stays on localhost.
const liveCookieName = "corral_live_token"

// liveTokenParam is the one-time query param that bootstraps the live cookie
// (mirrors the dashboard's ?token= dance). The iframe src carries it; the handler
// sets the cookie and redirects to strip it.
const liveTokenParam = "__live_token"

// handleLiveOrigin (GET /api/live-origin, on the DASHBOARD origin, cookie-authed)
// tells the SPA where Live View lives and the token to bootstrap it. The SPA points
// the iframe at base_url + ?__live_token=token; the live listener consumes the
// token and swaps it for a localhost-scoped cookie.
func (d *dashboardServer) handleLiveOrigin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"base_url": fmt.Sprintf("http://localhost:%d", d.livePort),
		"token":    d.liveToken,
	})
}

// liveRoutes is the handler for the second (localhost) listener. Only the live
// proxy and /healthz are reachable here — everything else 404s.
func (d *dashboardServer) liveRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/", d.requireLiveAuth(d.handleLiveRoot))
	return mux
}

// handleLiveRoot dispatches /p/<id>/live/<port>/<path…> to the shared live proxy.
// It reuses the SAME path structure as the dashboard's /p/<id>/live/ route, so all
// of hardenLiveResponse's URL rewriting (which prefixes with /p/<id>/live/<port>)
// works unchanged — the paths just resolve against the localhost origin now.
// Anything that isn't a live route is a 404 (this listener has no other surface).
func (d *dashboardServer) handleLiveRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/p/") {
		routeNotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/p/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" || len(parts) < 2 {
		routeNotFound(w, r)
		return
	}
	sub := parts[1]
	if !strings.HasPrefix(sub, "live/") {
		routeNotFound(w, r)
		return
	}
	d.handleLiveProxy(w, r, id, strings.TrimPrefix(sub, "live/"))
}

// requireLiveAuth gates the live listener with liveToken ONLY. It accepts the
// corral_live_token cookie, or a one-time ?__live_token=<t> that sets the cookie
// (host-only on localhost, HttpOnly, SameSite=Strict) and redirects to strip the
// param. The dashboard's token / apiToken are NEVER accepted here — the two
// origins keep separate credentials.
func (d *dashboardServer) requireLiveAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(liveCookieName); err == nil &&
			d.liveToken != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(d.liveToken)) == 1 {
			next(w, r)
			return
		}
		if tok := r.URL.Query().Get(liveTokenParam); tok != "" &&
			d.liveToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(d.liveToken)) == 1 {
			http.SetCookie(w, &http.Cookie{
				Name:     liveCookieName,
				Value:    d.liveToken,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				// No Domain → host-only on localhost; never sent to 127.0.0.1.
			})
			q := r.URL.Query()
			q.Del(liveTokenParam)
			clean := r.URL.Path
			if enc := q.Encode(); enc != "" {
				clean += "?" + enc
			}
			http.Redirect(w, r, clean, http.StatusFound)
			return
		}
		http.Error(w, "403 Forbidden — missing or invalid live-view token", http.StatusForbidden)
	}
}
