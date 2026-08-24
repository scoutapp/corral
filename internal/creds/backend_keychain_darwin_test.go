//go:build darwin

package creds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withKeychainBackend forces the keychain backend, skipping if /usr/bin/security
// isn't available (e.g. a stripped CI runner). Uses a unique host per test so
// concurrent/repeat runs don't collide, and cleans up the Keychain items.
func withKeychainBackend(t *testing.T) {
	t.Helper()
	t.Setenv("CORRAL_CREDS_BACKEND", "keychain")
	prev := selectedBackend
	selectedBackend = resolveBackend()
	t.Cleanup(func() { selectedBackend = prev })
	if selectedBackend.name() != "keychain" {
		t.Skip("keychain backend unavailable (no /usr/bin/security?)")
	}
}

// TestKeychainRoundTrip: the value goes to the Keychain and is NOT written to
// disk; LoadCredsMap injects it back. Then unset removes it from both.
func TestKeychainRoundTrip(t *testing.T) {
	withKeychainBackend(t)
	// A test-unique host so we don't touch real creds or collide across runs.
	host := "test-" + t.Name() + ".example.invalid"
	path := filepath.Join(t.TempDir(), "proxy-credentials.json")
	scope := scopeForPath(path)
	t.Cleanup(func() { _ = selectedBackend.deleteValue(scope, host) })

	in := map[string]map[string]string{
		host: {"header": "Authorization", "value": "token SECRET_KC"},
	}
	if err := WriteCredsMap(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	// On disk: metadata only, NO value.
	raw, _ := os.ReadFile(path)
	onDisk := map[string]map[string]string{}
	_ = json.Unmarshal(raw, &onDisk)
	if _, hasVal := onDisk[host]["value"]; hasVal {
		t.Fatalf("value must NOT be on disk with the keychain backend, got %s", raw)
	}
	if onDisk[host]["header"] != "Authorization" {
		t.Errorf("metadata should be on disk, got %s", raw)
	}

	// Load injects the value back from the Keychain.
	out, err := LoadCredsMap(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out[host]["value"] != "token SECRET_KC" {
		t.Errorf("loaded value = %q, want the keychain secret", out[host]["value"])
	}

	// Unset: remove the host from the map and write → value gone from Keychain.
	if err := WriteCredsMap(path, map[string]map[string]string{}); err != nil {
		t.Fatalf("unset write: %v", err)
	}
	if _, ok, _ := selectedBackend.getValue(scope, host); ok {
		t.Error("value should be deleted from the keychain after unset")
	}
}

// TestKeychainMigratesInlineValue: a pre-Keychain file with an inline value is
// hard-migrated on Load — the value moves into the Keychain and the file is
// rewritten without it.
func TestKeychainMigratesInlineValue(t *testing.T) {
	withKeychainBackend(t)
	host := "test-" + t.Name() + ".example.invalid"
	path := filepath.Join(t.TempDir(), "proxy-credentials.json")
	scope := scopeForPath(path)
	t.Cleanup(func() { _ = selectedBackend.deleteValue(scope, host) })

	// Simulate a legacy file: value inline on disk.
	legacy := map[string]map[string]string{host: {"header": "Authorization", "value": "token LEGACY"}}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	out, err := LoadCredsMap(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out[host]["value"] != "token LEGACY" {
		t.Errorf("migrated value = %q", out[host]["value"])
	}
	// The file must have been rewritten WITHOUT the inline value.
	raw, _ := os.ReadFile(path)
	onDisk := map[string]map[string]string{}
	_ = json.Unmarshal(raw, &onDisk)
	if _, hasVal := onDisk[host]["value"]; hasVal {
		t.Errorf("inline value should be stripped after migration, got %s", raw)
	}
	// And it now lives in the Keychain.
	if v, ok, _ := selectedBackend.getValue(scope, host); !ok || v != "token LEGACY" {
		t.Errorf("value should be in the keychain post-migration, got %q ok=%v", v, ok)
	}
}
