package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/scoutapp/corral/internal/prreview"
)

// PR-record prune (#local cleanup).
//
//	GET  /api/prs/prune?olderThanDays=30[&repo=<id>]  → { would_prune }
//	POST /api/prs/prune  { olderThanDays?:30, repo?:<id> }  → { pruned }
//
// Deletes LOCAL Corral DB records (the prs table + cascading blocks/links/chat)
// whose fetched_at is older than the cutoff. LOCAL ONLY — it never touches
// GitHub (no closing/deleting PRs upstream). Default cutoff 30 days; a floor of
// >= 1 day prevents wiping just-cached PRs. GET is a dry-run count; POST deletes.
// POST is mutating, so it's behind the API-writes gate like any other write.
const defaultPruneDays = 30

func (d *dashboardServer) handlePRPrune(w http.ResponseWriter, r *http.Request) {
	s, err := d.getStore()
	if err != nil {
		http.Error(w, "database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	svc := prreview.New(s)

	switch r.Method {
	case http.MethodGet:
		days := parsePruneDays(r.URL.Query().Get("olderThanDays"))
		if days < 1 {
			http.Error(w, "olderThanDays must be >= 1", http.StatusBadRequest)
			return
		}
		n, err := svc.PruneCount(days, r.URL.Query().Get("repo"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"would_prune": n, "olderThanDays": days})

	case http.MethodPost:
		var body struct {
			OlderThanDays *int   `json:"olderThanDays"`
			Repo          string `json:"repo"`
		}
		// A missing/empty body is fine — defaults apply.
		_ = json.NewDecoder(r.Body).Decode(&body)
		days := defaultPruneDays
		if body.OlderThanDays != nil {
			days = *body.OlderThanDays
		}
		if days < 1 {
			http.Error(w, "olderThanDays must be >= 1", http.StatusBadRequest)
			return
		}
		n, err := svc.Prune(days, body.Repo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"pruned": n, "olderThanDays": days})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// parsePruneDays parses the olderThanDays query param, defaulting to
// defaultPruneDays when absent/unparseable.
func parsePruneDays(raw string) int {
	if raw == "" {
		return defaultPruneDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultPruneDays
	}
	return n
}
