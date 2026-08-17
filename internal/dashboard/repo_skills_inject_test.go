package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/automations"
)

func TestInjectRepoAssets(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	svc := automations.New(s)

	// Seed a repo with two skills + agent context.
	svc.CreateRepoSkill("repo-1", "review-rules", "---\nname: review-rules\n---\nBe strict.")
	svc.CreateRepoSkill("repo-1", "test-guide", "---\nname: test-guide\n---\nUse table tests.")
	svc.SetRepoAgentContext("repo-1", "# Rules\nUse tabs.")

	ws := t.TempDir()
	d.injectRepoAssets(ws, []string{"repo-1"})

	// Skills landed as .corral/skills/<name>/SKILL.md.
	for _, name := range []string{"review-rules", "test-guide"} {
		p := filepath.Join(ws, ".corral", "skills", name, "SKILL.md")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("skill %q not written: %v", name, err)
			continue
		}
		if !strings.Contains(string(b), "name: "+name) {
			t.Errorf("skill %q content wrong: %s", name, b)
		}
	}

	// Agent context landed in CLAUDE.md under the marker.
	claude, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not written: %v", err)
	}
	if !strings.Contains(string(claude), corralContextMarker) || !strings.Contains(string(claude), "Use tabs.") {
		t.Errorf("CLAUDE.md missing marker/context: %s", claude)
	}
}

func TestInjectAgentContextPreservesRepoOwn(t *testing.T) {
	ws := t.TempDir()
	// The repo already commits its own CLAUDE.md.
	own := "# My repo\nBuild with make.\n"
	os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte(own), 0o644)

	writeAgentContext(ws, "Corral says: use tabs.")

	b, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	got := string(b)
	// The repo's own content is preserved ABOVE the corral marker.
	if !strings.Contains(got, "Build with make.") {
		t.Errorf("repo's own CLAUDE.md was clobbered: %s", got)
	}
	if !strings.Contains(got, corralContextMarker) || !strings.Contains(got, "use tabs.") {
		t.Errorf("corral block not appended: %s", got)
	}
	if strings.Index(got, "Build with make.") > strings.Index(got, corralContextMarker) {
		t.Errorf("repo's own content should be ABOVE the corral marker: %s", got)
	}
}

func TestInjectAgentContextReplacesPriorCorralBlock(t *testing.T) {
	ws := t.TempDir()
	// A prior injection is present; re-injecting must REPLACE it, not stack.
	writeAgentContext(ws, "first version")
	writeAgentContext(ws, "second version")

	b, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	got := string(b)
	if strings.Contains(got, "first version") {
		t.Errorf("old corral block not replaced: %s", got)
	}
	if !strings.Contains(got, "second version") {
		t.Errorf("new corral block missing: %s", got)
	}
	// Exactly one marker.
	if n := strings.Count(got, corralContextMarker); n != 1 {
		t.Errorf("expected exactly 1 marker, got %d: %s", n, got)
	}
}
