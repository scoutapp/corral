package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

// ensureHostClaudeConfigDir must give the host terminal a SEPARATE projects/ tree
// (so its transcripts don't collide with the sandbox's) while SHARING everything
// else from ~/.claude via symlinks (so auth/settings/plugins carry over).
func TestEnsureHostClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// CorralHome honors $CORRAL_HOME; point it inside the temp home so the config
	// dir lands somewhere disposable.
	t.Setenv("CORRAL_HOME", filepath.Join(home, ".corral"))

	// Seed a realistic ~/.claude: a shared file, a shared dir, and the projects/
	// dir that must NOT be linked.
	claude := filepath.Join(home, ".claude")
	mustMkdir(t, filepath.Join(claude, "plugins"))
	mustMkdir(t, filepath.Join(claude, "projects", "-some-old-slug"))
	mustWrite(t, filepath.Join(claude, "settings.json"), `{"x":1}`)
	mustWrite(t, filepath.Join(claude, ".credentials.json"), `{"token":"t"}`)

	cfgDir := ensureHostClaudeConfigDir()
	if cfgDir == "" {
		t.Fatal("ensureHostClaudeConfigDir returned empty")
	}

	// projects/ is a REAL dir we own, not a link to ~/.claude/projects.
	projects := filepath.Join(cfgDir, "projects")
	if fi, err := os.Lstat(projects); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("projects/ must be a real dir, got err=%v mode=%v", err, fi)
	}
	// And it must be empty of the sandbox's old slug — separation, not a mirror.
	if _, err := os.Stat(filepath.Join(projects, "-some-old-slug")); err == nil {
		t.Fatal("projects/ leaked the sandbox's transcript dir")
	}

	// settings.json and .credentials.json are symlinks pointing back at ~/.claude.
	for _, name := range []string{"settings.json", ".credentials.json", "plugins"} {
		lp := filepath.Join(cfgDir, name)
		target, err := os.Readlink(lp)
		if err != nil {
			t.Fatalf("%s should be a symlink: %v", name, err)
		}
		if want := filepath.Join(claude, name); target != want {
			t.Fatalf("%s links to %q, want %q", name, target, want)
		}
	}

	// Idempotent: a second call doesn't error or duplicate.
	if again := ensureHostClaudeConfigDir(); again != cfgDir {
		t.Fatalf("second call returned %q, want %q", again, cfgDir)
	}
}

// A non-symlink already sitting in the config dir (user-placed) must be left alone.
func TestEnsureHostClaudeConfigDirDoesNotClobber(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CORRAL_HOME", filepath.Join(home, ".corral"))
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"), `{}`)

	cfgDir := hostClaudeConfigDir()
	mustMkdir(t, cfgDir)
	// A real file the user dropped in, same name as a ~/.claude entry.
	real := filepath.Join(cfgDir, "settings.json")
	mustWrite(t, real, "USER DATA")

	ensureHostClaudeConfigDir()

	if fi, err := os.Lstat(real); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("user file was clobbered into a symlink: err=%v", err)
	}
	b, _ := os.ReadFile(real)
	if string(b) != "USER DATA" {
		t.Fatalf("user file content changed to %q", string(b))
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
