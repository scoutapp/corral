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
	dir := CorralHome()
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
		cfg := &ProjectConfig{} // no project extras → just the global default
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

func TestResolveSSHKeys_EmptyProjectStillLoadsGlobal(t *testing.T) {
	withHome(t, func(home string) {
		writeGlobalKeys(t, `["github_key"]`)
		cfg := &ProjectConfig{SSHKeys: []string{}} // no extras → global still loads
		got := cfg.ResolveSSHKeys()
		want := []string{filepath.Join(home, ".ssh", "github_key")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("empty project extras should still load global: got %v, want %v", got, want)
		}
	})
}

func TestResolveSSHKeys_ProjectUnionsWithGlobal(t *testing.T) {
	withHome(t, func(home string) {
		writeGlobalKeys(t, `["global_key"]`)
		cfg := &ProjectConfig{SSHKeys: []string{"project_key"}}
		got := cfg.ResolveSSHKeys()
		// Union: global first (always-on base), then project extras.
		want := []string{
			filepath.Join(home, ".ssh", "global_key"),
			filepath.Join(home, ".ssh", "project_key"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("project should union with global: got %v, want %v", got, want)
		}
	})
}

func TestResolveSSHKeys_UnionDedupsAcrossLayers(t *testing.T) {
	withHome(t, func(home string) {
		writeGlobalKeys(t, `["shared_key"]`)
		cfg := &ProjectConfig{SSHKeys: []string{"shared_key", "extra_key"}}
		got := cfg.ResolveSSHKeys()
		// shared_key appears in both layers → loaded once (from the global slot).
		want := []string{
			filepath.Join(home, ".ssh", "shared_key"),
			filepath.Join(home, ".ssh", "extra_key"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("union should dedup across layers: got %v, want %v", got, want)
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
