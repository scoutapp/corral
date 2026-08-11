package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()

	// A regular non-executable file.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// An executable file.
	exe := filepath.Join(dir, "exe")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{filepath.Join(dir, "does-not-exist"), false},
		{dir, false},   // directory
		{plain, false}, // not executable
		{exe, true},
	}
	for _, c := range cases {
		if got := isExecutable(c.path); got != c.want {
			t.Errorf("isExecutable(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestResolveClaudeBinEnvOverride(t *testing.T) {
	// Reset the cache so this test is deterministic regardless of order.
	claudeBinMu.Lock()
	claudeBinCached = ""
	claudeBinMu.Unlock()

	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fake\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// A valid CORRAL_CLAUDE_BIN is honored ahead of everything else.
	t.Setenv("CORRAL_CLAUDE_BIN", fake)
	got, err := resolveClaudeBin()
	if err != nil {
		t.Fatalf("resolveClaudeBin: %v", err)
	}
	if got != fake {
		t.Errorf("resolveClaudeBin() = %q, want the env-provided %q", got, fake)
	}

	// An invalid env path must NOT be returned (it falls through to other
	// strategies). We can't assert the fallthrough result on an arbitrary host,
	// but we can assert it never hands back the bogus path.
	claudeBinMu.Lock()
	claudeBinCached = ""
	claudeBinMu.Unlock()
	t.Setenv("CORRAL_CLAUDE_BIN", filepath.Join(dir, "nope"))
	got, _ = resolveClaudeBin()
	if got == filepath.Join(dir, "nope") {
		t.Errorf("resolveClaudeBin() returned a non-executable env path %q", got)
	}
}
