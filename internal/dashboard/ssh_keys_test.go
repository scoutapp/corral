package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSSHKeysStatusRouting checks the /p/<id>/sshkeys/status route guards without
// spawning an agent: unknown id 404s, unauth 403s. (A real status probe needs a
// registered workspace + config and is exercised via manual/e2e testing.)
func TestSSHKeysStatusRouting(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	// Unknown id → 404.
	r, _ := http.NewRequest("GET", srv.URL+"/p/deadbeef/sshkeys/status", strings.NewReader(""))
	r.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id GET /sshkeys/status = %d, want 404", resp.StatusCode)
	}

	// Unauthenticated → 403.
	r2, _ := http.NewRequest("GET", srv.URL+"/p/deadbeef/sshkeys/status", nil)
	resp2, _ := http.DefaultClient.Do(r2)
	if resp2 != nil {
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden {
			t.Errorf("unauth GET /sshkeys/status = %d, want 403", resp2.StatusCode)
		}
	}
}

// The status JSON shape must stay stable — the config.js UI keys off these fields.
func TestSSHKeysStatusShape(t *testing.T) {
	var s sshKeysStatus
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"configured", "loaded", "keys", "count"} {
		if !strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("status JSON missing %q field: %s", field, b)
		}
	}
}
