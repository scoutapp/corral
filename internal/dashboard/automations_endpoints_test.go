package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAutomationsAPI exercises the /api/actions + /api/hooks + /api/runs routes
// against the real mux: create an action, list it, run it (a claude_prompt-less
// bash action would need a binary, so we run a capability action with no
// provider wired — it records a run with an error, which is still a valid,
// recorded run). The point is the HTTP surface, not the executor internals.
func TestAutomationsAPI(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// Create an action.
	resp := do("POST", "/api/actions", `{"name":"Approve","kind":"capability","spec":"{\"capability\":\"pr-approve\"}"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/actions status = %d", resp.StatusCode)
	}
	var created struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == 0 || created.Name != "Approve" {
		t.Fatalf("unexpected created action: %+v", created)
	}

	// List — should include it (global scope).
	resp = do("GET", "/api/actions", "")
	var list struct {
		Actions []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"actions"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Actions) != 1 || list.Actions[0].ID != created.ID {
		t.Fatalf("list did not return created action: %+v", list)
	}

	// Update.
	resp = do("PUT", "/api/actions/1", `{"name":"Approve+","spec":"{\"capability\":\"pr-approve\",\"body\":\"LGTM\"}"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Run it ad-hoc with a context that has no owner/pr → capability fails, but a
	// run is recorded (that's the contract). Endpoint returns 200 with a
	// StepResult whose status is "error".
	resp = do("POST", "/api/actions/1:run", `{"vars":{}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf(":run status = %d", resp.StatusCode)
	}
	var runRes struct {
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&runRes)
	resp.Body.Close()
	if runRes.Status != "error" {
		t.Errorf("expected capability with no target to error, got %q", runRes.Status)
	}

	// The run should show up in /api/runs.
	resp = do("GET", "/api/runs", "")
	var runs struct {
		Runs []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	json.NewDecoder(resp.Body).Decode(&runs)
	resp.Body.Close()
	if len(runs.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs.Runs))
	}

	// Bind a hook, list it, then delete it.
	resp = do("POST", "/api/hooks", `{"event":"pr.approve","targetKind":"action","targetId":1,"enabled":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/hooks status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do("GET", "/api/hooks?event=pr.approve", "")
	var hooks struct {
		Hooks []struct {
			ID int64 `json:"id"`
		} `json:"hooks"`
	}
	json.NewDecoder(resp.Body).Decode(&hooks)
	resp.Body.Close()
	if len(hooks.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks.Hooks))
	}

	resp = do("DELETE", "/api/hooks/1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/hooks/1 status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Delete the action.
	resp = do("DELETE", "/api/actions/1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/actions/1 status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}
