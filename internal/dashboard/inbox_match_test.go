package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/scoutapp/corral/internal/repos"
)

func TestWorkspaceMatchesRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://github.com/scoutapp/apm.git")

	apm := &repos.Repo{URL: "https://github.com/scoutapp/apm"}
	if !workspaceMatchesRepo(ws, apm, "scoutapp/apm") {
		t.Error("workspace with scoutapp/apm origin should match the apm repo")
	}
	other := &repos.Repo{URL: "https://github.com/scoutapp/other"}
	if workspaceMatchesRepo(ws, other, "scoutapp/other") {
		t.Error("should NOT match a different repo")
	}

	// A workspace with no git repo doesn't match.
	empty := filepath.Join(t.TempDir(), "nogit")
	os.MkdirAll(empty, 0o755)
	if workspaceMatchesRepo(empty, apm, "scoutapp/apm") {
		t.Error("non-git workspace should not match")
	}
}
