package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestScriptEnvEndpoint checks /api/scripts/env reports host=true and returns a
// valid CLI status list — every entry is installed (present) with one of the
// three statuses, and git (if present) is no_auth.
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
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"clis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if !env.Host || env.Note == "" {
		t.Errorf("expected host=true + a note, got host=%v note=%q", env.Host, env.Note)
	}
	valid := map[string]bool{"authed": true, "unauthed": true, "no_auth": true}
	for _, c := range env.CLIs {
		if !valid[c.Status] {
			t.Errorf("cli %s has invalid status %q", c.Name, c.Status)
		}
		// git, if listed, has no auth concept.
		if c.Name == "git" && c.Status != "no_auth" {
			t.Errorf("git should be no_auth, got %q", c.Status)
		}
		// docker must never be authed — it has no auth probe (presence only).
		if c.Name == "docker" && c.Status == "authed" {
			t.Error("docker should not report authed (no meaningful auth probe)")
		}
	}
}
