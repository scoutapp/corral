package dashboard

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestCmdAPIRoundTrip drives a real test dashboard through CmdAPI via --url/--token
// and asserts a documented GET round-trips (the spec itself), and that a non-2xx
// surfaces as an error.
func TestCmdAPIRoundTrip(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	// GET /api/openapi.json should succeed and print JSON to stdout.
	var apiErr error
	out := captureStdout(t, func() {
		apiErr = CmdAPI([]string{"--url", srv.URL, "--token", "tok", "GET", "/api/openapi.json"})
	})
	if apiErr != nil {
		t.Fatalf("CmdAPI GET openapi: %v", apiErr)
	}
	if !strings.Contains(out, `"openapi"`) {
		t.Errorf("expected openapi JSON on stdout, got: %s", truncate(out, 120))
	}

	// A bogus path returns the router's 404 → CmdAPI returns a non-nil error.
	err := CmdAPI([]string{"--url", srv.URL, "--token", "tok", "GET", "/api/does-not-exist"})
	if err == nil {
		t.Error("expected an error for a 404 path, got nil")
	}
}

// TestCmdAPIAutoDiscovers confirms the CLI reads base URL + token from the
// persisted dashboard state when no --url/--token/env are given.
func TestCmdAPIAutoDiscovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)
	t.Setenv("CORRAL_DASH_URL", "")
	t.Setenv("CORRAL_DASH_TOKEN", "")

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	// Persist dashboard state pointing at the test server's port.
	u, _ := url.Parse(srv.URL)
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	if err := writeDashboardState(&DashboardState{Port: port, Token: "tok"}); err != nil {
		t.Fatal(err)
	}

	// resolveDashboardTarget should now find both from disk.
	base, token, err := resolveDashboardTarget("", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(base, u.Port()) || token != "tok" {
		t.Errorf("auto-discovery wrong: base=%q token=%q", base, token)
	}
}

// TestCmdAPINoDashboard errors clearly when nothing is running and no overrides.
func TestCmdAPINoDashboard(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	t.Setenv("CORRAL_DASH_URL", "")
	t.Setenv("CORRAL_DASH_TOKEN", "")
	_, _, err := resolveDashboardTarget("", "")
	if err == nil {
		t.Error("expected an error when no dashboard is running")
	}
}

