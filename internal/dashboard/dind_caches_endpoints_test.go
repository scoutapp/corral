package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDindCachesEndpointValidation exercises the request-shape validation that
// doesn't touch docker (name/project required, invalid name rejected, unknown
// project 404). The docker-backed happy path (snapshot/list/delete real volumes)
// is covered by the dindcache package + manual/integration testing — a unit test
// can't create a docker volume in CI.
func TestDindCachesEndpointValidation(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("sess").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Missing project → 400.
	resp := do("POST", "/api/dind/caches", `{"name":"pg-16"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing project: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Invalid cache name → 400 (before any docker/project lookup).
	resp = do("POST", "/api/dind/caches", `{"name":"has spaces","project":"whatever"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid name: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid name but unknown project → 404.
	resp = do("POST", "/api/dind/caches", `{"name":"pg-16","project":"nope-nope"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
