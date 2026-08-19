package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestConductorWorkerValidation checks the create route resolves and rejects a
// missing prompt (without spawning a real claude, which a valid prompt would).
func TestConductorWorkerValidation(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	do := func(bodyJSON string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/conductor/workers", strings.NewReader(bodyJSON))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST worker: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Missing prompt → 400 (and the route is matched, not unmatched).
	code, body := do(`{"title":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty prompt: want 400, got %d (%s)", code, body)
	}
	if strings.Contains(body, "unmatched") {
		t.Fatalf("worker route did not resolve: %s", body)
	}
}

// TestNewWorkerJobID sanity-checks the id shape.
func TestNewWorkerJobID(t *testing.T) {
	id := newWorkerJobID()
	if !strings.HasPrefix(id, "worker-") {
		t.Fatalf("worker id should be worker-prefixed, got %q", id)
	}
}
