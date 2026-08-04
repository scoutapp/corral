package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// withHome points HOME + SANDCLAUDE_HOME at a temp dir for the duration of fn,
// so ExpandSSHKeyPath (~ expansion) and GlobalSSHKeysPath are hermetic.
func withHome(t *testing.T, fn func(home string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SANDCLAUDE_HOME", filepath.Join(home, ".sandclaude"))
	fn(home)
}

func writeGlobalKeys(t *testing.T, keys string) {
	t.Helper()
	dir := SandclaudeHome()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ssh-keys.json"), []byte(keys), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExpandSSHKeyPath(t *testing.T) {
	withHome(t, func(home string) {
		cases := map[string]string{
			"":                     "",
			"  ":                   "",
			"~":                    home,
			"~/x":                  filepath.Join(home, "x"),
			"id_ed25519":           filepath.Join(home, ".ssh", "id_ed25519"),
			"sub/key":              filepath.Join(home, ".ssh", "sub/key"),
			"/abs/path/key":        "/abs/path/key",
			"/abs/../abs/path/key": "/abs/path/key", // cleaned
		}
		for in, want := range cases {
			if got := ExpandSSHKeyPath(in); got != want {
				t.Errorf("ExpandSSHKeyPath(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func TestResolveSSHKeys_InheritGlobalWhenNil(t *testing.T) {
	withHome(t, func(home string) {
		writeGlobalKeys(t, `["github_key", "~/other"]`)
		cfg := &ProjectConfig{} // SSHKeys nil → inherit global
		got := cfg.ResolveSSHKeys()
		want := []string{
			filepath.Join(home, ".ssh", "github_key"),
			filepath.Join(home, "other"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestResolveSSHKeys_ExplicitEmptyOverridesGlobal(t *testing.T) {
	withHome(t, func(_ string) {
		writeGlobalKeys(t, `["github_key"]`)
		cfg := &ProjectConfig{SSHKeys: []string{}} // explicit empty → NO keys
		if got := cfg.ResolveSSHKeys(); len(got) != 0 {
			t.Errorf("explicit empty should yield no keys, got %v", got)
		}
	})
}

func TestResolveSSHKeys_ProjectReplacesGlobal(t *testing.T) {
	withHome(t, func(home string) {
		writeGlobalKeys(t, `["global_key"]`)
		cfg := &ProjectConfig{SSHKeys: []string{"project_key"}}
		got := cfg.ResolveSSHKeys()
		want := []string{filepath.Join(home, ".ssh", "project_key")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("project should replace global: got %v, want %v", got, want)
		}
	})
}

func TestResolveSSHKeys_Dedup(t *testing.T) {
	withHome(t, func(home string) {
		cfg := &ProjectConfig{SSHKeys: []string{"k", "~/.ssh/k", "k"}}
		got := cfg.ResolveSSHKeys()
		want := []string{filepath.Join(home, ".ssh", "k")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("dedup failed: got %v, want %v", got, want)
		}
	})
}

func TestResolveSSHKeys_NoGlobalNoProject(t *testing.T) {
	withHome(t, func(_ string) {
		cfg := &ProjectConfig{} // nil, and no global file
		if got := cfg.ResolveSSHKeys(); len(got) != 0 {
			t.Errorf("expected no keys, got %v", got)
		}
	})
}

// The nil-vs-empty distinction must survive a JSON round-trip through the config
// file, since that's the whole reason ssh_keys has no omitempty.
func TestSSHKeys_JSONRoundTripPreservesEmptyVsAbsent(t *testing.T) {
	withHome(t, func(_ string) {
		dir := t.TempDir()

		// explicit empty
		if err := WriteConfig(dir, &ProjectConfig{Workspace: "/w", SSHKeys: []string{}}); err != nil {
			t.Fatal(err)
		}
		got, err := ReadConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.SSHKeys == nil {
			t.Error("explicit empty ssh_keys became nil after round-trip (would wrongly inherit global)")
		}
		if len(got.SSHKeys) != 0 {
			t.Errorf("explicit empty ssh_keys gained entries: %v", got.SSHKeys)
		}
	})
}
