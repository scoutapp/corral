package creds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withFileBackend forces the inline file backend so the test is hermetic and
// runs identically on every platform (no Keychain).
func withFileBackend(t *testing.T) {
	t.Helper()
	t.Setenv("CORRAL_CREDS_BACKEND", "file")
	prev := selectedBackend
	selectedBackend = resolveBackend()
	t.Cleanup(func() { selectedBackend = prev })
	if selectedBackend.name() != "file" {
		t.Fatalf("expected file backend, got %q", selectedBackend.name())
	}
}

// TestFileBackendRoundTrip: with the file backend, values are written inline and
// read back unchanged (the original, Linux behavior).
func TestFileBackendRoundTrip(t *testing.T) {
	withFileBackend(t)
	path := filepath.Join(t.TempDir(), "proxy-credentials.json")

	in := map[string]map[string]string{
		"api.github.com": {"header": "Authorization", "value": "token ghp_x"},
	}
	if err := WriteCredsMap(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The value IS inline on disk with the file backend.
	raw, _ := os.ReadFile(path)
	if got := map[string]map[string]string{}; json.Unmarshal(raw, &got) != nil || got["api.github.com"]["value"] != "token ghp_x" {
		t.Fatalf("value should be inline on disk, got %s", raw)
	}
	out, err := LoadCredsMap(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out["api.github.com"]["value"] != "token ghp_x" {
		t.Errorf("round-trip value = %q", out["api.github.com"]["value"])
	}
}

// TestScopeForPath: global path → "global"; a project path → a distinct,
// project-derived scope (so two projects' same-host secrets don't collide).
func TestScopeForPath(t *testing.T) {
	if s := scopeForPath(GlobalCredentialsPath()); s != "global" {
		t.Errorf("global scope = %q, want global", s)
	}
	p := scopeForPath("/some/project/.corral/project/proxy-credentials.json")
	if p == "global" || p == "" {
		t.Errorf("project scope should be distinct + non-empty, got %q", p)
	}
}

// TestStripValues removes only the value field, preserving metadata.
func TestStripValues(t *testing.T) {
	in := map[string]map[string]string{
		"h": {"header": "Authorization", "value": "secret"},
	}
	out := stripValues(in)
	if _, has := out["h"]["value"]; has {
		t.Error("value should be stripped")
	}
	if out["h"]["header"] != "Authorization" {
		t.Error("metadata should be preserved")
	}
	// Original must be untouched.
	if in["h"]["value"] != "secret" {
		t.Error("stripValues must not mutate the input")
	}
}
