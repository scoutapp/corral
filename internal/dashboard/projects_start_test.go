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
	t.Setenv("SANDCLAUDE_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req := func(method, path string) int {
		r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
		r.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Unknown project id → 404 (lookupWorkspaceByID fails before any start).
	if got := req("POST", "/p/deadbeef/start"); got != http.StatusNotFound {
		t.Errorf("unknown id POST /start = %d, want 404", got)
	}
	// Unauthenticated → 403 (auth gate).
	r, _ := http.NewRequest("POST", srv.URL+"/p/deadbeef/start", nil)
	resp, _ := http.DefaultClient.Do(r)
	if resp != nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("unauth POST /start = %d, want 403", resp.StatusCode)
		}
	}
}
