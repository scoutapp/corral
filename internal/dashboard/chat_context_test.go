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

// withContextHint now only prepends the per-page context marker on the first turn;
// the operating rules moved to the system prompt (conductorSystemPrompt).
func TestWithContextHint(t *testing.T) {
	hint := "The user is viewing repo acme/widget."

	// First turn with a hint: the context marker is prepended, prompt preserved.
	got := withContextHint("what's broken?", hint, true)
	if !strings.HasPrefix(got, "[Context: "+hint+"]") || !strings.HasSuffix(got, "what's broken?") {
		t.Errorf("first-turn hint not prepended: %q", got)
	}
	// The operating rules must NOT be in the user message anymore (they're system).
	if strings.Contains(got, "CONDUCTOR") || strings.Contains(got, "corral-question") {
		t.Errorf("operating rules should no longer be in the user prompt: %q", got)
	}

	// Later turns / no hint: passed through verbatim.
	if got := withContextHint("and this one?", hint, false); got != "and this one?" {
		t.Errorf("later turn should be unchanged, got %q", got)
	}
	if got := withContextHint("hello", "", true); got != "hello" {
		t.Errorf("no-hint first turn should be just the prompt, got %q", got)
	}
}

// TestConductorSystemPrompt: the standing rules that ride on EVERY turn via
// --append-system-prompt (so a resumed conductor keeps them).
func TestConductorSystemPrompt(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir()) // no repos → repo block omitted, rest present
	sp := conductorSystemPrompt()
	for _, want := range []string{
		"CONDUCTOR", "/projects/create", // sandbox-routing
		"corral-question",                        // ask-the-user convention
		"BUILD A REPO'S DOCKER IMAGE", "/api/dind/caches", // image build
		"VERIFY LIVE VIEW", "verify-live-view", // live-view render check
		"FIRE-AND-FORGET", "Monitor", // turn-lifetime warning
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("conductor system prompt missing %q", want)
		}
	}
}
