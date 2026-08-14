package dashboard

import (
	_ "embed"
	"net/http"
)

// openapiSpec is the hand-authored OpenAPI 3.1 description of the /api/*
// automations control plane, embedded so it ships with the binary. It's the
// contract the future corral CLI and macro tooling consume to talk to the
// dashboard. Kept in sync with the actual routes by hand (the surface is small
// and stable); a mismatch is a review-catchable bug, not a silent drift.
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
