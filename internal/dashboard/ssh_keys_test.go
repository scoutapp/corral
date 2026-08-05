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

// TestSSHKeysSelectAuthGate is a regression guard for the SSH control plane's
// most sensitive property: the container must never be able to reach these
// endpoints, and the only thing standing between it and them is requireAuth. A
// future refactor that accidentally registered /sshkeys/select outside the auth
// wrapper would silently open a hole. Assert a tokenless POST is rejected (403),
// and an authenticated POST to an unknown id is merely not-found (404) — i.e. the
// route IS wired AND IS gated. No agent is spawned (unknown id 404s first).
func TestSSHKeysSelectAuthGate(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	body := `{"ssh_keys":["/Users/x/.ssh/id_ed25519"]}`

	// Unauthenticated → 403 (proves the route sits behind requireAuth).
	r, _ := http.NewRequest("POST", srv.URL+"/p/deadbeef/sshkeys/select", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauth POST /sshkeys/select = %d, want 403", resp.StatusCode)
	}

	// A bad token is also rejected (constant-time compare, no bypass).
	rb, _ := http.NewRequest("POST", srv.URL+"/p/deadbeef/sshkeys/select", strings.NewReader(body))
	rb.Header.Set("Content-Type", "application/json")
	rb.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "wrong"})
	respb, err := http.DefaultClient.Do(rb)
	if err != nil {
		t.Fatal(err)
	}
	respb.Body.Close()
	if respb.StatusCode != http.StatusForbidden {
		t.Errorf("bad-token POST /sshkeys/select = %d, want 403", respb.StatusCode)
	}

	// Authenticated but unknown id → 404 (proves the route IS registered; it got
	// past auth and into handleRoot's project lookup rather than 403-ing).
	r2, _ := http.NewRequest("POST", srv.URL+"/p/deadbeef/sshkeys/select", strings.NewReader(body))
	r2.Header.Set("Content-Type", "application/json")
	r2.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
	resp2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("auth POST /sshkeys/select unknown id = %d, want 404", resp2.StatusCode)
	}
}

// The status JSON shape must stay stable — the config.js UI keys off these fields.
func TestSSHKeysStatusShape(t *testing.T) {
	var s sshKeysStatus
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"configured", "loaded", "keys", "count", "container_stale"} {
		if !strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("status JSON missing %q field: %s", field, b)
		}
	}
}
