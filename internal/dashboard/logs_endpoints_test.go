package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/store"
)

// TestLogsAPI seeds a few log rows into the shared store, then exercises the
// /api/logs endpoint: paging, filters, search, and facets.
func TestLogsAPI(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())

	// Seed via a logger over the same store the server opens.
	s, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	lg := applog.New(s, false)
	lg.Info(applog.CatAI, "ai.analyze", "Analyzed PR #42 widget", map[string]any{"pr": 42})
	lg.Info(applog.CatPRAction, "pr.approve", "Approved PR #42", nil)
	lg.Log(applog.Entry{Level: applog.LevelInfo, Category: applog.CatProject, Event: "project.start", Message: "Started x", ProjectID: "proj-1"})
	s.Close()

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	type page struct {
		Logs []struct {
			ID                                  int64 `json:"id"`
			Category, Event, Message, ProjectID string
		} `json:"logs"`
		NextCursor int64 `json:"nextCursor"`
	}
	decode := func(resp *http.Response) page {
		var p page
		json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		return p
	}

	// Note: the request-logging middleware also logs each /api/logs GET (event
	// http.request), so tests filter to the seeded categories for determinism —
	// which itself proves the http-request logging works.

	// Category filter (isolates our seeded rows from the http.* request logs).
	if p := decode(get("/api/logs?category=ai")); len(p.Logs) != 1 || p.Logs[0].Category != "ai" {
		t.Errorf("category filter failed: %+v", p.Logs)
	}
	// Project filter.
	if p := decode(get("/api/logs?project=proj-1")); len(p.Logs) != 1 {
		t.Errorf("project filter failed: %+v", p.Logs)
	}
	// Search.
	if p := decode(get("/api/logs?q=widget")); len(p.Logs) != 1 || p.Logs[0].Event != "ai.analyze" {
		t.Errorf("search failed: %+v", p.Logs)
	}
	// The middleware logged our GETs under category=http.
	if p := decode(get("/api/logs?category=http&limit=100")); len(p.Logs) == 0 {
		t.Error("expected http request logs from the middleware")
	}

	// Keyset paging within the pr-action category (2 seeded? no — 1). Use the
	// project category filter which has exactly 1 to prove single-page cursor=0,
	// and http (many) to prove multi-page cursor advances.
	pHTTP := decode(get("/api/logs?category=http&limit=2"))
	if len(pHTTP.Logs) != 2 || pHTTP.NextCursor == 0 {
		t.Fatalf("http page1 wrong: %+v", pHTTP)
	}
	pHTTP2 := decode(get("/api/logs?category=http&limit=2&before=" + itoaI64(pHTTP.NextCursor)))
	// Keyset: page2 rows are strictly older (smaller id) than page1's oldest.
	if len(pHTTP2.Logs) == 0 || pHTTP2.Logs[0].ID >= pHTTP.Logs[len(pHTTP.Logs)-1].ID {
		t.Fatalf("http page2 should advance by keyset: p1oldest=%d p2newest=%d",
			pHTTP.Logs[len(pHTTP.Logs)-1].ID, pHTTP2.Logs[0].ID)
	}

	// Facets include the seeded categories + http (4 total) and our 1 project.
	resp := get("/api/logs/facets")
	var facets struct {
		Categories []string `json:"categories"`
		Projects   []string `json:"projects"`
	}
	json.NewDecoder(resp.Body).Decode(&facets)
	resp.Body.Close()
	if len(facets.Categories) < 4 || len(facets.Projects) != 1 {
		t.Errorf("facets wrong: %+v", facets)
	}
}

func itoaI64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
