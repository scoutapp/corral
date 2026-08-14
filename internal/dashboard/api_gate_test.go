package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// newGateTestServer builds a dashboard with known session + API tokens and a
// CORRAL_HOME so global settings read/write to a temp dir.
func newGateTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	d.apiToken = "apitok"
	return httptest.NewServer(d.routes())
}

func doReq(t *testing.T, srv *httptest.Server, method, path, token string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func setApiWrites(t *testing.T, enabled bool) {
	t.Helper()
	gs := config.ReadGlobalSettings()
	gs.ApiWritesEnabled = enabled
	if err := config.WriteGlobalSettings(gs); err != nil {
		t.Fatalf("write global settings: %v", err)
	}
}

func TestApiWritesGate(t *testing.T) {
	srv := newGateTestServer(t)
	defer srv.Close()

	// A mutating path that exists (POST /p/<id>/start). With a bogus project it
	// won't succeed, but the GATE runs before the handler — so we assert on 403
	// (gated) vs not-403 (allowed through to the handler, which then 404s).
	const writePath = "/p/nonexistent/start"

	// Writes disabled (default): API token → 403 from the gate.
	setApiWrites(t, false)
	if code := doReq(t, srv, http.MethodPost, writePath, "apitok"); code != http.StatusForbidden {
		t.Errorf("api write with gate OFF: got %d, want 403", code)
	}

	// The BROWSER (session token) is never gated — same disabled setting, the
	// gate lets it through to the handler (which 404s the bogus project).
	if code := doReq(t, srv, http.MethodPost, writePath, "sess"); code == http.StatusForbidden {
		t.Errorf("session write with gate OFF was 403 — the browser must never be gated")
	}

	// A READ with the API token is always allowed regardless of the setting.
	if code := doReq(t, srv, http.MethodGet, "/api/flows", "apitok"); code == http.StatusForbidden {
		t.Errorf("api read with gate OFF was 403 — reads must always pass")
	}

	// Enable writes: the API token now passes the gate (reaches the handler → 404
	// for the bogus project, NOT 403).
	setApiWrites(t, true)
	if code := doReq(t, srv, http.MethodPost, writePath, "apitok"); code == http.StatusForbidden {
		t.Errorf("api write with gate ON was still 403")
	}
}

// TestApiGateOnlyMutating confirms the gate keys on the METHOD: a GET with the
// API token is never blocked even when writes are disabled.
func TestApiGateOnlyMutating(t *testing.T) {
	srv := newGateTestServer(t)
	defer srv.Close()
	setApiWrites(t, false)

	for _, p := range []string{"/api/flows", "/api/logs", "/status"} {
		if code := doReq(t, srv, http.MethodGet, p, "apitok"); code == http.StatusForbidden {
			t.Errorf("GET %s with API token was 403 — reads must always pass", p)
		}
	}
}
