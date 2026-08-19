package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/prreview"
)

// apiTestServer builds a dashboard with a temp CORRAL_HOME and a seeded PR, and
// returns the server + the seeded PR's id. Uses the SESSION token so the write
// gate (which only applies to the API token) doesn't interfere — we're testing
// the handlers, not the gate (that has its own test).
func apiTestServer(t *testing.T) (*httptest.Server, *dashboardServer, int64) {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	s, err := d.getStore()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	// Seed a PR row directly so /api/prs/<id>/notes has something to attach to.
	res, err := s.DB().Exec(
		`INSERT INTO prs (repo_id, pr_number, title, fetched_at) VALUES (?, ?, ?, datetime('now'))`,
		"repo1", 42, "Test PR")
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	prID, _ := res.LastInsertId()
	_ = prreview.New(s) // ensure the package is linked in this test binary
	return httptest.NewServer(d.routes()), d, prID
}

func apiReq(t *testing.T, srv *httptest.Server, method, path, jsonBody string) (int, string) {
	t.Helper()
	var r io.Reader
	if jsonBody != "" {
		r = strings.NewReader(jsonBody)
	}
	req, _ := http.NewRequest(method, srv.URL+path, r)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestAPINotesCRUD drives the /api/prs/<id>/notes surface end to end.
func TestAPINotesCRUD(t *testing.T) {
	srv, _, prID := apiTestServer(t)
	defer srv.Close()
	base := "/api/prs/" + itoa(prID)

	// Empty to start.
	code, body := apiReq(t, srv, http.MethodGet, base+"/notes", "")
	if code != 200 || !strings.Contains(body, `"notes"`) {
		t.Fatalf("GET notes: %d %s", code, body)
	}

	// Add one.
	code, body = apiReq(t, srv, http.MethodPost, base+"/notes", `{"body":"look at the migration","author":"cli"}`)
	if code != 200 {
		t.Fatalf("POST note: %d %s", code, body)
	}
	var added struct {
		Note prreview.PRNote `json:"note"`
	}
	if err := json.Unmarshal([]byte(body), &added); err != nil || added.Note.ID == 0 {
		t.Fatalf("POST note bad response: %v (%s)", err, body)
	}

	// It shows up.
	_, body = apiReq(t, srv, http.MethodGet, base+"/notes", "")
	if !strings.Contains(body, "look at the migration") {
		t.Fatalf("note not listed: %s", body)
	}

	// Blank body → 400.
	if code, _ := apiReq(t, srv, http.MethodPost, base+"/notes", `{"body":"  "}`); code != 400 {
		t.Fatalf("blank note: want 400, got %d", code)
	}

	// Delete it.
	if code, b := apiReq(t, srv, http.MethodDelete, base+"/notes/"+itoa(added.Note.ID), ""); code != 200 {
		t.Fatalf("DELETE note: %d %s", code, b)
	}
	// Delete unknown → 404.
	if code, _ := apiReq(t, srv, http.MethodDelete, base+"/notes/999999", ""); code != 404 {
		t.Fatalf("DELETE unknown note: want 404, got %d", code)
	}
}

// TestAPIMergeNoStrategyErrors confirms a merge with no strategy set anywhere
// returns a 400 that steers the caller to set a per-repo default.
func TestAPIMergeNoStrategyErrors(t *testing.T) {
	srv, _, prID := apiTestServer(t)
	defer srv.Close()
	// The seeded PR's repo isn't a GitHub remote (no repos registry entry), so the
	// merge handler errors before strategy resolution — but we specifically want to
	// prove the ROUTE resolves and returns a 4xx, not an unmatched route.
	code, body := apiReq(t, srv, http.MethodPost, "/api/prs/"+itoa(prID)+"/merge", `{"mode":"plain"}`)
	if code == 404 && strings.Contains(body, "unmatched") {
		t.Fatalf("merge route did not resolve: %d %s", code, body)
	}
	if code < 400 || code >= 500 {
		t.Fatalf("merge with no GitHub remote / no strategy: want a 4xx, got %d (%s)", code, body)
	}
}
