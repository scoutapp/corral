package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatCapabilityNullUntilSet(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")

	// Unset on first run → not configured (this is what triggers the modal).
	if cap, ok := d.ChatCapability(); ok {
		t.Errorf("expected unconfigured on first run, got %q ok=true", cap)
	}
	// Unset → read-only tools (the safe posture before the user chooses).
	cap, ok := d.ChatCapability()
	if got := globalChatTools(cap, ok); strings.Join(got, ",") != "Read,Grep,Glob" {
		t.Errorf("unset should be read-only tools, got %v", got)
	}

	// Choose read-only explicitly → configured, still read-only tools.
	if err := d.SetChatCapability(CapabilityReadOnly); err != nil {
		t.Fatal(err)
	}
	if cap, ok := d.ChatCapability(); !ok || cap != CapabilityReadOnly {
		t.Errorf("after set readonly: cap=%q ok=%v", cap, ok)
	}

	// Upgrade to act → Bash added.
	if err := d.SetChatCapability(CapabilityAct); err != nil {
		t.Fatal(err)
	}
	cap, ok = d.ChatCapability()
	if !ok || cap != CapabilityAct {
		t.Fatalf("after set act: cap=%q ok=%v", cap, ok)
	}
	if got := globalChatTools(cap, ok); strings.Join(got, ",") != "Read,Grep,Glob,Bash" {
		t.Errorf("act should add Bash, got %v", got)
	}
}

func TestChatCapabilityNormalizesUnknown(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	// Anything not "act" normalizes to read-only (fail safe).
	if err := d.SetChatCapability("garbage"); err != nil {
		t.Fatal(err)
	}
	if cap, _ := d.ChatCapability(); cap != CapabilityReadOnly {
		t.Errorf("unknown capability should normalize to readonly, got %q", cap)
	}
}

func TestChatCapabilityEndpoint(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	srv := httptest.NewServer(newDashboardServer("sess").routes())
	defer srv.Close()

	get := func() (string, bool) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/chat/capability", nil)
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Capability *string `json:"capability"`
			Configured bool    `json:"configured"`
		}
		decodeJSON(t, resp, &out)
		if out.Capability == nil {
			return "", out.Configured
		}
		return *out.Capability, out.Configured
	}

	// First run: capability null, configured=false.
	if _, configured := get(); configured {
		t.Error("expected configured=false on first run")
	}

	// PUT act.
	req, _ := http.NewRequest("PUT", srv.URL+"/api/chat/capability", strings.NewReader(`{"capability":"act"}`))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	// Now configured=act.
	if cap, configured := get(); !configured || cap != "act" {
		t.Errorf("after PUT: cap=%q configured=%v", cap, configured)
	}
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
