package dashboard

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestSplitAPIArgs covers the flag-position independence: METHOD + path can come
// before OR after flags. The regression this guards: Go's flag package stops at
// the first positional, so `POST /path -d '{...}'` used to silently drop -d and
// send an empty body (→ 400).
func TestSplitAPIArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantFlags  []string
	}{
		{"flags after positionals", []string{"POST", "/api/actions", "-d", "{}"}, "POST", "/api/actions", []string{"-d", "{}"}},
		{"flags before positionals", []string{"-d", "{}", "POST", "/api/actions"}, "POST", "/api/actions", []string{"-d", "{}"}},
		{"flags interleaved", []string{"POST", "-d", "{}", "/api/actions"}, "POST", "/api/actions", []string{"-d", "{}"}},
		{"inline --data=value", []string{"POST", "/api/actions", "--data={}"}, "POST", "/api/actions", []string{"--data={}"}},
		{"bool flag doesn't eat path", []string{"GET", "-i", "/api/flows"}, "GET", "/api/flows", []string{"-i"}},
		{"no flags", []string{"GET", "/api/flows"}, "GET", "/api/flows", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, p, flags, err := splitAPIArgs(tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m != tc.wantMethod || p != tc.wantPath {
				t.Errorf("method/path = %q %q, want %q %q", m, p, tc.wantMethod, tc.wantPath)
			}
			if strings.Join(flags, " ") != strings.Join(tc.wantFlags, " ") {
				t.Errorf("flags = %v, want %v", flags, tc.wantFlags)
			}
		})
	}

	// Too few positionals is an error.
	if _, _, _, err := splitAPIArgs([]string{"GET"}); err == nil {
		t.Error("expected error for missing path")
	}
	// A stray third positional is rejected rather than silently ignored.
	if _, _, _, err := splitAPIArgs([]string{"GET", "/a", "/b"}); err == nil {
		t.Error("expected error for extra positional")
	}
}

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

