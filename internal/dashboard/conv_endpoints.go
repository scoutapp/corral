package dashboard

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/convstore"
)

// Conversation read API — the queryable surface over the captured conversations
// DB. Routed under /api/conversations by handleAPI. Reads only (capture happens
// at the stream sites), so these are not gated by the API-writes setting.
//
//	GET /api/conversations                 keyset list (?before,limit,origin,project,repo,trace,parent,q)
//	GET /api/conversations/facets          distinct origins + projects (filter menus)
//	GET /api/conversations/search?q=       deep search across all conversations (alias of list?q=)
//	GET /api/conversations/<id>            one conversation (metadata)
//	GET /api/conversations/<id>/messages   its messages (?q= filters within the conversation)

// handleConversations dispatches /api/conversations[/...]. rest is the path after
// "conversations" ("" | "facets" | "search" | "<id>" | "<id>/messages").
func (d *dashboardServer) handleConversations(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cs, err := d.getConvStore()
	if err != nil {
		http.Error(w, "conversations database unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()

	switch {
	case rest == "" || rest == "search":
		// Both are the same keyset list; /search is a friendlier alias for a q-only
		// call. Filters compose with q.
		page, err := cs.List(listQueryFromURL(q))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, page)

	case rest == "facets":
		origins, _ := cs.Origins()
		projects, _ := cs.Projects()
		if origins == nil {
			origins = []string{}
		}
		if projects == nil {
			projects = []string{}
		}
		writeJSON(w, map[string]any{"origins": origins, "projects": projects})

	default:
		// /<id>, /<id>/messages, or /<id>/chain.
		idStr := rest
		messages, chain := false, false
		if s, ok := strings.CutSuffix(rest, "/messages"); ok {
			idStr, messages = s, true
		} else if s, ok := strings.CutSuffix(rest, "/chain"); ok {
			idStr, chain = s, true
		}
		id, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			http.NotFound(w, r)
			return
		}
		if chain {
			convs, err := cs.Chain(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"conversations": convs})
			return
		}
		if messages {
			msgs, err := cs.Messages(id, q.Get("q"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"messages": msgs})
			return
		}
		conv, err := cs.Get(id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"conversation": conv})
	}
}

// listQueryFromURL builds a convstore.ListQuery from URL params (shared shape
// with the logs list: before/limit keyset + filters + q).
func listQueryFromURL(q url.Values) convstore.ListQuery {
	return convstore.ListQuery{
		Before:  parseInt64(q.Get("before")),
		Limit:   int(parseInt64(q.Get("limit"))),
		Origin:  q.Get("origin"),
		Project: q.Get("project"),
		Repo:    q.Get("repo"),
		Trace:   q.Get("trace"),
		Parent:  parseInt64(q.Get("parent")),
		Q:       q.Get("q"),
	}
}

// parseInt64 parses a base-10 int64, returning 0 on empty/invalid (the keyset
// convention: 0 = newest page / unset filter).
func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
