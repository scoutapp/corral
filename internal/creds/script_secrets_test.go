package creds

import (
	"testing"
)

func TestScriptScopeFromPath(t *testing.T) {
	p := ScriptSecretsPath(42)
	if s := scopeForPath(p); s != "script:42" {
		t.Errorf("scopeForPath(%q) = %q, want script:42", p, s)
	}
	// Non-script paths keep their scope.
	if s := scopeForPath(GlobalCredentialsPath()); s != "global" {
		t.Errorf("global scope = %q", s)
	}
}

func TestScriptIDFromPath(t *testing.T) {
	if id, ok := scriptIDFromPath(ScriptSecretsPath(7)); !ok || id != "7" {
		t.Errorf("scriptIDFromPath = %q, %v", id, ok)
	}
	if _, ok := scriptIDFromPath("/somewhere/else/proxy-credentials.json"); ok {
		t.Error("non-script path should not parse as a script id")
	}
}

// TestScriptSecretsRoundTrip: write → env + values resolve; unset removes.
// File backend keeps the test hermetic.
func TestScriptSecretsRoundTrip(t *testing.T) {
	withFileBackend(t)
	t.Setenv("CORRAL_HOME", t.TempDir())

	in := map[string]map[string]string{
		"FRESHDESK_API_KEY": {"kind": "env", "name": "FRESHDESK_API_KEY", "value": "sk-fd-123"},
		"FRESHDESK_DOMAIN":  {"kind": "env", "name": "FRESHDESK_DOMAIN", "value": "scoutapm"},
		"UNSET_VAR":         {"kind": "env", "name": "UNSET_VAR"}, // no value → not injected
	}
	if err := WriteScriptSecrets(9, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	env, err := ScriptSecretEnv(9)
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	got := map[string]bool{}
	for _, e := range env {
		got[e] = true
	}
	if !got["FRESHDESK_API_KEY=sk-fd-123"] || !got["FRESHDESK_DOMAIN=scoutapm"] {
		t.Errorf("env missing expected entries: %v", env)
	}
	for _, e := range env {
		if e == "UNSET_VAR=" || len(e) > 9 && e[:9] == "UNSET_VAR" {
			t.Errorf("valueless secret should not be injected: %q", e)
		}
	}

	vals, _ := ScriptSecretValues(9)
	vset := map[string]bool{}
	for _, v := range vals {
		vset[v] = true
	}
	if !vset["sk-fd-123"] || !vset["scoutapm"] {
		t.Errorf("values missing: %v", vals)
	}
}
