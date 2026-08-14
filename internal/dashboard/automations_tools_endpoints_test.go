package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestToolAdapterGated verifies the tool surface is off by default and only
// exposes/invokes tools once the user enables the permission flag.
func TestToolAdapterGated(t *testing.T) {
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

	// A prompt action to expose (deterministic, no external deps).
	resp := do("POST", "/api/actions", `{"name":"Greeter","kind":"claude_prompt","spec":"{\"template\":\"hi {{who}}\"}"}`)
	resp.Body.Close()

	// Disabled by default: manifest reports enabled=false, empty tools.
	resp = do("GET", "/api/tools", "")
	var man struct {
		Enabled bool  `json:"enabled"`
		Tools   []any `json:"tools"`
	}
	json.NewDecoder(resp.Body).Decode(&man)
	resp.Body.Close()
	if man.Enabled || len(man.Tools) != 0 {
		t.Fatalf("tools should be disabled by default: %+v", man)
	}

	// Invoke while disabled → 403.
	resp = do("POST", "/api/tools/corral_greeter:invoke", `{"who":"world"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("invoke while disabled should 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Enable the permission flag.
	if err := config.WriteGlobalSettings(&config.GlobalSettings{AutomationsToolsEnabled: true}); err != nil {
		t.Fatal(err)
	}

	// Now the manifest lists the tool.
	resp = do("GET", "/api/tools", "")
	json.NewDecoder(resp.Body).Decode(&man)
	resp.Body.Close()
	if !man.Enabled || len(man.Tools) != 1 {
		t.Fatalf("tools should be enabled + listed: %+v", man)
	}

	// And invoke runs the action.
	resp = do("POST", "/api/tools/corral_greeter:invoke", `{"who":"world"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invoke status = %d", resp.StatusCode)
	}
	var run struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	if run.Status != "ok" || run.Output != "hi world" {
		t.Errorf("tool invoke result wrong: %+v", run)
	}
}
