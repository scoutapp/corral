package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackrothrock/sandclaude/internal/config"
)

// fakeAssets sets SANDCLAUDE_HOME to a temp dir laid out so config.AssetsDir()
// resolves there, with a minimal allowlist seed InitProject can copy.
func fakeAssets(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SANDCLAUDE_HOME", home)
	sandbox := filepath.Join(home, "assets", "sandbox")
	if err := os.MkdirAll(filepath.Join(sandbox, "allowlist-proxy"), 0755); err != nil {
		t.Fatal(err)
	}
	// AssetsDir()'s looksLikeSandbox checks for Dockerfile + allowlist-proxy/.
	os.WriteFile(filepath.Join(sandbox, "Dockerfile"), []byte("FROM scratch\n"), 0644)
	os.WriteFile(filepath.Join(sandbox, "allowlist-proxy", "allowed-domains.txt"),
		[]byte("api.anthropic.com\ngithub.com\n"), 0644)
}

func TestInitProject(t *testing.T) {
	fakeAssets(t)
	ws := t.TempDir()

	cfg, err := InitProject(ws, InitOptions{ProxyEnabled: true, DindEnabled: true, DindPorts: []string{"3000:3000"}})
	if err != nil {
		t.Fatalf("InitProject: %v", err)
	}

	// Config carries the workspace + options.
	if cfg.Workspace != ws || !cfg.ProxyEnabled || !cfg.DindEnabled || len(cfg.DindPorts) != 1 {
		t.Errorf("config not populated as expected: %+v", cfg)
	}

	// On-disk artifacts exist under the workspace.
	projDir := config.ProjectDirFor(ws)
	for _, p := range []string{
		filepath.Join(projDir, "config.json"),
		filepath.Join(projDir, ".allowlist-key"),
		filepath.Join(ws, ".sandclaude", "allowed-domains.txt"),
		filepath.Join(ws, ".sandclaude", "allowed-domains.txt.enc"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// The key is 64 hex chars (32 bytes).
	key, _ := os.ReadFile(filepath.Join(projDir, ".allowlist-key"))
	if len(key) != 64 {
		t.Errorf("allowlist key = %d chars, want 64 hex", len(key))
	}

	// Re-init on an already-initialized workspace errors.
	if _, err := InitProject(ws, InitOptions{}); err == nil {
		t.Error("expected error re-initializing an existing project")
	}
}

func TestInitProjectRequiresWorkspace(t *testing.T) {
	if _, err := InitProject("", InitOptions{}); err == nil {
		t.Error("expected error for empty workspace")
	}
}
