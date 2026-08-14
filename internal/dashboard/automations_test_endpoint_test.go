package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestActionTestEndpoint exercises POST /api/actions:test — running an unsaved
// bash action and getting its output back, without persisting anything.
func TestActionTestEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	do := func(body string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/api/actions:test", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return resp
	}

	// A bash step that echoes an injected context var.
	resp := do(`{"kind":"bash","spec":"{\"script\":\"echo pr=$CORRAL_PR_NUMBER\"}","context":{"vars":{"pr_number":"7"}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var res struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if res.Status != "ok" || res.Output != "pr=7" {
		t.Fatalf("unexpected test result: %+v", res)
	}

	// Nothing was persisted — no runs recorded.
	req, _ := http.NewRequest("GET", srv.URL+"/api/runs", nil)
	req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
	rr, _ := http.DefaultClient.Do(req)
	var runs struct {
		Runs []any `json:"runs"`
	}
	json.NewDecoder(rr.Body).Decode(&runs)
	rr.Body.Close()
	if len(runs.Runs) != 0 {
		t.Errorf("test-run should not persist a run, got %d", len(runs.Runs))
	}

	// A failing script reports error + output.
	resp = do(`{"kind":"bash","spec":"{\"script\":\"echo boom; exit 2\"}"}`)
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if res.Status != "error" || !strings.Contains(res.Output, "boom") {
		t.Errorf("expected error+output, got %+v", res)
	}
}
