package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMergeJobsEndpoints checks the merge-job routes are wired: an empty list,
// and a DELETE of an unknown id 404s.
func TestMergeJobsEndpoints(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	do := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// GET /merge-jobs → 200 with an empty list.
	code, body := do(http.MethodGet, "/merge-jobs")
	if code != http.StatusOK {
		t.Fatalf("GET /merge-jobs: got %d, want 200 (body=%s)", code, body)
	}
	var listResp struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(body), &listResp); err != nil {
		t.Fatalf("GET /merge-jobs: bad JSON: %v (body=%s)", err, body)
	}
	if len(listResp.Jobs) != 0 {
		t.Fatalf("GET /merge-jobs: expected empty list, got %d", len(listResp.Jobs))
	}

	// DELETE an unknown job → 404.
	if code, _ := do(http.MethodDelete, "/merge-jobs/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("DELETE unknown job: got %d, want 404", code)
	}
}
