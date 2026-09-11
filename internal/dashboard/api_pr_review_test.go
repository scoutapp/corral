package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAPIPRReviewRoutes exercises the conductor-facing review surface: the stored
// review is null before a run, the analysis status carries a "review" field, and
// the start route is reachable (returns started, or 502 when claude is absent —
// either proves the route is wired, which is what we're testing here).
func TestAPIPRReviewRoutes(t *testing.T) {
	srv, _, prID := apiTestServer(t)
	defer srv.Close()
	base := "/api/prs/" + itoa(prID)

	// GET review before any run → {"review": null}.
	code, body := apiReq(t, srv, http.MethodGet, base+"/review", "")
	if code != http.StatusOK {
		t.Fatalf("GET review = %d, want 200 (%s)", code, body)
	}
	var got struct {
		Review *struct {
			Markdown string `json:"markdown"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode review: %v (%s)", err, body)
	}
	if got.Review != nil {
		t.Errorf("expected null review before a run, got %+v", got.Review)
	}

	// The analysis status must expose a "review" job alongside enrich + risk, so a
	// conductor can poll it.
	code, body = apiReq(t, srv, http.MethodGet, base+"/analysis", "")
	if code != http.StatusOK {
		t.Fatalf("GET analysis = %d, want 200", code)
	}
	for _, want := range []string{`"review"`, `"enrich"`, `"risk"`} {
		if !strings.Contains(body, want) {
			t.Errorf("analysis status missing %s: %s", want, body)
		}
	}

	// POST review starts the job (200 started) or 502 if the claude CLI isn't on
	// this machine — both mean the route is wired. A 404 would mean it isn't.
	code, body = apiReq(t, srv, http.MethodPost, base+"/review", "{}")
	if code != http.StatusOK && code != http.StatusBadGateway {
		t.Fatalf("POST review = %d, want 200 or 502 (%s)", code, body)
	}
	if code == http.StatusOK && !strings.Contains(body, `"review"`) {
		t.Errorf("started review response should name the kind: %s", body)
	}
}

// TestAPIPRReviewStatusRoute wires GET review-status. The seeded PR's repo isn't
// a resolvable GitHub remote in the test, so it returns {status:null} (200) — the
// graceful-degrade path — proving the route is reachable without needing network.
func TestAPIPRReviewStatusRoute(t *testing.T) {
	srv, _, prID := apiTestServer(t)
	defer srv.Close()

	code, body := apiReq(t, srv, http.MethodGet, "/api/prs/"+itoa(prID)+"/review-status", "")
	if code != http.StatusOK {
		t.Fatalf("GET review-status = %d, want 200 (%s)", code, body)
	}
	if !strings.Contains(body, `"status"`) {
		t.Errorf("review-status response missing status field: %s", body)
	}
}
