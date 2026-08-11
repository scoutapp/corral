package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStartProjectRouting checks the /p/<id>/start route's guards WITHOUT
// triggering a real container start: unknown id 404s and a GET is rejected,
// both of which return before any docker/start work. (A real cold start needs
// Docker + a workspace and is covered by manual/e2e testing.)
func TestStartProjectRouting(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req := func(method, path string) int {
		r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
		r.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Unknown project id → 404 (lookupWorkspaceByID fails before any start/stop).
	for _, action := range []string{"start", "stop"} {
		if got := req("POST", "/p/deadbeef/"+action); got != http.StatusNotFound {
			t.Errorf("unknown id POST /%s = %d, want 404", action, got)
		}
		// GET is rejected (both require POST).
		if got := req("GET", "/p/deadbeef/"+action); got == http.StatusOK {
			t.Errorf("GET /%s should not be 200", action)
		}
	}
	// Unauthenticated → 403 (auth gate) for both.
	for _, action := range []string{"start", "stop"} {
		r, _ := http.NewRequest("POST", srv.URL+"/p/deadbeef/"+action, nil)
		resp, _ := http.DefaultClient.Do(r)
		if resp != nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("unauth POST /%s = %d, want 403", action, resp.StatusCode)
			}
		}
	}
}
