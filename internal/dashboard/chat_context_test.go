package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChatReposContext seeds a repo registry in a temp CORRAL_HOME and checks the
// conductor's repo context lists the id/name/branch (so it doesn't hunt on disk).
func TestChatReposContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)

	// No repos yet → empty context (must not disturb the prompt).
	if got := chatReposContext(); got != "" {
		t.Errorf("no repos should yield empty context, got %q", got)
	}

	// Seed one repo directly into the registry file.
	reg := `{"repos":[{"id":"abc123","name":"core-agent","url":"https://github.com/scoutapp/core-agent","default_branch":"master"}]}`
	if err := os.WriteFile(filepath.Join(home, "repos.json"), []byte(reg), 0600); err != nil {
		t.Fatal(err)
	}
	got := chatReposContext()
	for _, want := range []string{"core-agent", "abc123", "master", "/projects/create"} {
		if !strings.Contains(got, want) {
			t.Errorf("repo context missing %q: %q", want, got)
		}
	}
}

func TestWithContextHint(t *testing.T) {
	hint := "The user is viewing repo acme/widget."

	// First turn, GLOBAL chat, with a hint: the context marker is prepended, the
	// question-asking guidance and the conductor rule are included, and the
	// original prompt is preserved.
	got := withContextHint("what's broken?", hint, true, true)
	if !strings.HasPrefix(got, "[Context: "+hint+"]") || !strings.Contains(got, "what's broken?") {
		t.Errorf("first-turn hint not prepended: %q", got)
	}
	if !strings.Contains(got, "corral-question") {
		t.Errorf("first-turn prompt should carry the question guidance: %q", got)
	}
	if !strings.Contains(got, "CONDUCTOR") || !strings.Contains(got, "/projects/create") {
		t.Errorf("global first-turn prompt should carry the conductor/sandbox rule: %q", got)
	}
	if !strings.Contains(got, "BUILD A REPO'S DOCKER IMAGE") || !strings.Contains(got, "/api/dind/caches") {
		t.Errorf("global first-turn prompt should carry the image-build capability: %q", got)
	}
	if !strings.Contains(got, "VERIFY LIVE VIEW") || !strings.Contains(got, "verify-live-view") {
		t.Errorf("global first-turn prompt should carry the live-view verify guidance: %q", got)
	}
	// The turn-lifetime warning must be present so the conductor doesn't start a
	// Monitor/background task and end its turn (it would be orphaned).
	if !strings.Contains(got, "FIRE-AND-FORGET") || !strings.Contains(got, "Monitor") {
		t.Errorf("global first-turn prompt should carry the turn-lifetime warning: %q", got)
	}

	// Later turns: nothing prepended (context + guidance already carried via
	// --resume) — the prompt is passed through verbatim.
	if got := withContextHint("and this one?", hint, false, true); got != "and this one?" {
		t.Errorf("later turn should be unchanged, got %q", got)
	}
	if got := withContextHint("hello", "", false, false); got != "hello" {
		t.Errorf("later turn with no hint should be unchanged, got %q", got)
	}

	// First turn with NO context hint, GLOBAL: no [Context:] marker, but the
	// question guidance + conductor rule still apply and the prompt is preserved.
	got = withContextHint("hello", "", true, true)
	if strings.Contains(got, "[Context:") {
		t.Errorf("no-hint first turn should not have a context marker: %q", got)
	}
	if !strings.Contains(got, "corral-question") || !strings.HasSuffix(got, "hello") {
		t.Errorf("no-hint first turn should carry guidance + the prompt: %q", got)
	}

	// First turn, PROJECT chat (isGlobal=false): it already runs inside a sandbox,
	// so the conductor rule is NOT injected — but the question guidance still is.
	got = withContextHint("fix the bug", hint, true, false)
	if strings.Contains(got, "CONDUCTOR") {
		t.Errorf("project chat should NOT get the conductor rule: %q", got)
	}
	if !strings.Contains(got, "corral-question") || !strings.HasSuffix(got, "fix the bug") {
		t.Errorf("project first turn should still carry the question guidance + prompt: %q", got)
	}
}
