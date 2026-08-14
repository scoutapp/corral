package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChatLoadsCorralApiSkill verifies the host chat wires the corral-api skill
// via --plugin-dir when the host bundle carries it, and omits the flag (no crash)
// when it doesn't. This is the load path that keeps the skill scoped to the
// Corral chat process instead of installing it into ~/.claude/skills.
func TestChatLoadsCorralApiSkill(t *testing.T) {
	// Absent bundle → no --plugin-dir, no panic.
	t.Setenv("CORRAL_HOME", t.TempDir())
	if got := strings.Join(buildClaudeArgs("hi", nil, ""), " "); strings.Contains(got, "--plugin-dir") {
		t.Errorf("expected no --plugin-dir with an empty bundle, got: %s", got)
	}

	// Present bundle (a host dir with the skill's plugin manifest) → flag emitted
	// pointing at it.
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)
	skillDir := filepath.Join(home, "assets", "host", "skills", "corral-api")
	if err := os.MkdirAll(filepath.Join(skillDir, ".claude-plugin"), 0755); err != nil {
		t.Fatal(err)
	}
	// HostAssetsDir identifies a host bundle by proxy-addon.py.
	if err := os.WriteFile(filepath.Join(home, "assets", "host", "proxy-addon.py"), []byte("# stub\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".claude-plugin", "plugin.json"), []byte(`{"name":"corral-api","skills":["./"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: corral-api\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	args := buildClaudeArgs("hi", nil, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--plugin-dir") || !strings.Contains(joined, skillDir) {
		t.Errorf("expected --plugin-dir %s in args, got: %s", skillDir, joined)
	}
}
