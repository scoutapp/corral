package dashboard

import (
	_ "embed"
	"net/http"
)

// openapiSpec is the hand-authored OpenAPI 3.1 description of the dashboard's
// host control plane — a curated surface (repos, projects, GitHub issues,
// automations, logs/traces) covering the mutating + high-value REST the
// `corral api` CLI and the host Claude skill drive. It deliberately omits the
// machinery endpoints (WebSockets, files/git/mitm/ssh/terminal). Embedded so it
// ships with the binary and is served at GET /api/openapi.json.
//
// It is kept honest by TestOpenAPINoDrift, which exercises the live router and
// fails if the spec documents a path the server doesn't actually serve — so this
// is real drift protection, not an honor system.
//
//go:embed openapi.json
var openapiSpec []byte

// handleOpenAPI serves the spec at GET /api/openapi.json.
func (d *dashboardServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(openapiSpec)
}
