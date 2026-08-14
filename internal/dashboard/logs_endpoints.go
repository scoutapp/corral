package dashboard

import (
	"net/http"
	"strconv"

	"github.com/scoutapp/corral/internal/applog"
)

// applog returns a central logger over the shared store, or nil if the DB can't
// be opened (a nil *applog.Logger is a safe no-op, so emit sites can call it
// unconditionally). Debug persistence follows the dashboard's debug setting;
// for now it's off (info+ only) — retention settings will expose this later.
func (d *dashboardServer) applog() *applog.Logger {
	s, err := d.getStore()
	if err != nil {
		return nil
	}
	return applog.New(s, false)
}

// GET /api/logs — the host-wide application log, newest-first, keyset-paginated.
//
//	?limit=50            page size (default 50, max 500)
//	?before=<id>         keyset cursor: rows older than this id (0/absent = newest)
//	?category=ai         filter by category
//	?project=<id>        filter by project
//	?repo=<id>           filter by repo
//	?level=error         filter by level
//	?q=<text>            free-text search over message + meta
//
// → { logs:[…], nextCursor:<id|0> }   (nextCursor 0 = no older rows)
//
// GET /api/logs/facets → { categories:[…], projects:[…] } for the filter menus.
func (d *dashboardServer) handleLogs(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	l := d.applog()
	if l == nil {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
		return
	}

	if rest == "facets" {
		cats, _ := l.Categories()
		projs, _ := l.Projects()
		if cats == nil {
			cats = []string{}
		}
		if projs == nil {
			projs = []string{}
		}
		writeJSON(w, map[string]any{"categories": cats, "projects": projs})
		return
	}

	q := r.URL.Query()
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	page, err := l.Query(applog.Query{
		Before:   atoi(q.Get("before")),
		Limit:    int(atoi(q.Get("limit"))),
		Category: q.Get("category"),
		Project:  q.Get("project"),
		Repo:     q.Get("repo"),
		Level:    q.Get("level"),
		Q:        q.Get("q"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, page)
}
