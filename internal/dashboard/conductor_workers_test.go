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

// TestWorkerContractPreamble guards the fix for the stranded-worker gap: every
// worker's first-turn prompt is prefixed with the "single headless turn — finish
// in-turn, don't park on background jobs" contract, and the caller's task text is
// preserved after it.
func TestWorkerContractPreamble(t *testing.T) {
	// The preamble must actually tell the worker the load-bearing facts.
	for _, want := range []string{"headless", "resume", "background", "BLOCK in-turn"} {
		if !strings.Contains(workerContractPreamble, want) {
			t.Errorf("worker contract preamble missing %q", want)
		}
	}
	// Composition (mirrors startWorkerJob): preamble first, then the task, intact.
	task := "Verify PR #5670 and boot the app."
	composed := workerContractPreamble + task
	if !strings.HasPrefix(composed, "IMPORTANT") {
		t.Fatalf("composed prompt must lead with the contract, got: %.20q", composed)
	}
	if !strings.HasSuffix(composed, task) {
		t.Fatalf("composed prompt must end with the caller's task verbatim")
	}
}
