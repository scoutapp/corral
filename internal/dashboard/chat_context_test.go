package dashboard

import (
	"strings"
	"testing"
)

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
