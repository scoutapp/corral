package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestScriptEnvEndpoint checks /api/scripts/env reports host=true and probes the
// CLI set (git is virtually always present in CI; at minimum the shape is right).
func TestScriptEnvEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/scripts/env", nil)
	req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var env struct {
		Host bool   `json:"host"`
		Note string `json:"note"`
		CLIs []struct {
			Name          string `json:"name"`
			Available     bool   `json:"available"`
			Authenticated *bool  `json:"authenticated"`
		} `json:"clis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if !env.Host {
		t.Error("expected host=true")
	}
	if env.Note == "" {
		t.Error("expected a note describing the host runtime")
	}
	if len(env.CLIs) == 0 {
		t.Fatal("expected the CLI probe list")
	}
	// git should be discoverable and has no auth concept (authenticated == nil).
	var sawGit bool
	for _, c := range env.CLIs {
		if c.Name == "git" {
			sawGit = true
			if c.Authenticated != nil {
				t.Error("git should have no auth probe (authenticated == nil)")
			}
		}
	}
	if !sawGit {
		t.Error("git should be in the probe list")
	}
}
