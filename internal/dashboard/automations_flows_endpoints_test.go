package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestFlowsAPI drives the /api/flows lifecycle through the real mux: create two
// actions, create a flow, append a step, run the flow, and list flows.
func TestFlowsAPI(t *testing.T) {
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

	// Two prompt actions (deterministic, no external deps).
	mkAction := func(name, tmpl string) int64 {
		resp := do("POST", "/api/actions",
			`{"name":"`+name+`","kind":"claude_prompt","spec":"{\"template\":\"`+tmpl+`\"}"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create action %s: status %d", name, resp.StatusCode)
		}
		var a struct {
			ID int64 `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&a)
		resp.Body.Close()
		return a.ID
	}
	a1 := mkAction("first", "hello {{who}}")
	a2 := mkAction("second", "echo {{steps.one.output}}")

	// Create a flow with the first step inline.
	resp := do("POST", "/api/flows",
		`{"name":"greet","steps":[{"actionId":`+itoa(a1)+`,"stepKey":"one"}]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/flows status = %d", resp.StatusCode)
	}
	var flow struct {
		ID    int64 `json:"id"`
		Steps []struct {
			StepKey string `json:"stepKey"`
		} `json:"steps"`
	}
	json.NewDecoder(resp.Body).Decode(&flow)
	resp.Body.Close()
	if flow.ID == 0 || len(flow.Steps) != 1 {
		t.Fatalf("unexpected created flow: %+v", flow)
	}

	// Append the second step.
	resp = do("POST", "/api/flows/"+itoa(flow.ID)+"/steps",
		`{"actionId":`+itoa(a2)+`,"position":1,"stepKey":"two"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("append step status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Run the flow: step one renders "hello world"; step two chains it.
	resp = do("POST", "/api/flows/"+itoa(flow.ID)+":run", `{"vars":{"who":"world"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("flow :run status = %d", resp.StatusCode)
	}
	var run struct {
		Status string `json:"status"`
		Steps  []struct {
			Output string `json:"output"`
		} `json:"steps"`
	}
	json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	if run.Status != "ok" || len(run.Steps) != 2 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.Steps[0].Output != "hello world" {
		t.Errorf("step 1 render wrong: %q", run.Steps[0].Output)
	}
	if run.Steps[1].Output != "echo hello world" {
		t.Errorf("step 2 should chain step 1 output, got %q", run.Steps[1].Output)
	}

	// List flows.
	resp = do("GET", "/api/flows", "")
	var list struct {
		Flows []struct{ ID int64 } `json:"flows"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Flows) != 1 {
		t.Fatalf("expected 1 flow, got %d", len(list.Flows))
	}
}

// itoa is a tiny local helper to keep the test JSON readable.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
