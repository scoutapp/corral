package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/mcp"
)

// TestOpenAPISpecValid asserts the embedded spec is well-formed JSON, is
// OpenAPI 3.x, and documents the core paths (now absolute, servers root "/") —
// so a malformed edit is caught at test time rather than by a downstream CLI.
func TestOpenAPISpecValid(t *testing.T) {
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if doc.OpenAPI == "" || doc.OpenAPI[0] != '3' {
		t.Errorf("expected OpenAPI 3.x, got %q", doc.OpenAPI)
	}
	for _, p := range []string{"/api/actions", "/api/actions/{id}:run", "/api/flows/{id}:run", "/repos", "/p/{id}/start", "/gh/issues/create"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("spec missing path %q", p)
		}
	}
}

// TestOpenAPINoDrift is the guard that keeps the spec honest: every path it
// documents must resolve to a REAL route. Because the dashboard dispatches by
// manual prefix matching (no route registry to enumerate), we exercise the live
// mux — substitute a concrete value for each {param} and send the documented
// method.
//
// A path that isn't served at all returns the router's terminal 404, which
// carries the "X-Corral-Route: unmatched" marker (routeNotFound). A path that IS
// served but whose resource doesn't exist (project/1, action/1) returns a plain
// resource 404 WITHOUT that marker. We key on the marker, so a real route that
// resource-404s still passes — only a genuinely unmatched path fails.
//
// One-directional by design: it catches spec→routes drift (the spec lying about
// what exists). It can't enumerate every real route to catch the reverse (an
// endpoint with no doc), which is fine — the curated spec is deliberately a
// subset, and documenting a dead path is the dangerous direction.
func TestOpenAPINoDrift(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("tok")
	// Stub the mcp client so exercising GET /api/mcp doesn't shell out to the real
	// `claude mcp list` (slow, host-dependent). We only assert the route resolves.
	d.mcpClientOverride = mcp.NewWithRunner(func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	})
	// Don't spawn a real tmux OAuth session when the drift test POSTs the login
	// route — just prove the route resolves.
	d.loginSpawnerOverride = func(bin, name string) error { return nil }
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &doc); err != nil {
		t.Fatalf("spec parse: %v", err)
	}

	// Concrete stand-ins for path templates. The values needn't exist — we only
	// assert the route resolves (not 404), not that the resource is found.
	fill := strings.NewReplacer(
		"{id}", "1",
		"{noteId}", "1",
		"{name}", "x",
		"{key}", "project.start",
		"{traceId}", "abc",
	)

	for specPath, methods := range doc.Paths {
		concrete := fill.Replace(specPath)
		if strings.Contains(concrete, "{") {
			t.Errorf("path %q has an un-substituted {param} — extend fill{}", specPath)
			continue
		}
		for method := range methods {
			m := strings.ToUpper(method)
			if m == "PARAMETERS" { // shared params object, not an operation
				continue
			}
			req, err := http.NewRequest(m, srv.URL+concrete, strings.NewReader("{}"))
			if err != nil {
				t.Errorf("%s %s: build request: %v", m, concrete, err)
				continue
			}
			req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("%s %s: %v", m, concrete, err)
				continue
			}
			unmatched := resp.Header.Get("X-Corral-Route") == "unmatched"
			resp.Body.Close()
			if unmatched {
				t.Errorf("spec documents %s %s but the router doesn't serve it — drift", m, specPath)
			}
		}
	}
}

// TestOpenAPIServed checks the endpoint returns the spec as JSON.
func TestOpenAPIServed(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/openapi.json", nil)
	req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("served spec not JSON: %v", err)
	}
	if _, ok := doc["openapi"]; !ok {
		t.Error("served spec missing openapi field")
	}
}
