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

	// Full list — newest first.
	all := decode(get("/api/logs"))
	if len(all.Logs) != 3 || all.Logs[0].Event != "project.start" {
		t.Fatalf("full list wrong: %+v", all.Logs)
	}

	// Category filter.
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
	// Keyset paging: limit=2 then use the cursor.
	p1 := decode(get("/api/logs?limit=2"))
	if len(p1.Logs) != 2 || p1.NextCursor == 0 {
		t.Fatalf("page1 wrong: %+v", p1)
	}
	p2 := decode(get("/api/logs?limit=2&before=" + itoaI64(p1.NextCursor)))
	if len(p2.Logs) != 1 || p2.NextCursor != 0 {
		t.Fatalf("page2 wrong: %+v", p2)
	}

	// Facets.
	resp := get("/api/logs/facets")
	var facets struct {
		Categories []string `json:"categories"`
		Projects   []string `json:"projects"`
	}
	json.NewDecoder(resp.Body).Decode(&facets)
	resp.Body.Close()
	if len(facets.Categories) != 3 || len(facets.Projects) != 1 {
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
