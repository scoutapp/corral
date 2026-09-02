package dashboard

import (
	"strings"
	"testing"
)

func TestWithContextHint(t *testing.T) {
	hint := "The user is viewing repo acme/widget."

	// First turn with a hint: the context marker is prepended, the question-asking
	// guidance is included, and the original prompt is preserved.
	got := withContextHint("what's broken?", hint, true)
	if !strings.HasPrefix(got, "[Context: "+hint+"]") || !strings.Contains(got, "what's broken?") {
		t.Errorf("first-turn hint not prepended: %q", got)
	}
	if !strings.Contains(got, "corral-question") {
		t.Errorf("first-turn prompt should carry the question guidance: %q", got)
	}

	// Later turns: nothing prepended (context + guidance already carried via
	// --resume) — the prompt is passed through verbatim.
	if got := withContextHint("and this one?", hint, false); got != "and this one?" {
		t.Errorf("later turn should be unchanged, got %q", got)
	}
	if got := withContextHint("hello", "", false); got != "hello" {
		t.Errorf("later turn with no hint should be unchanged, got %q", got)
	}

	// First turn with NO context hint: no [Context:] marker, but the question
	// guidance still applies (it's page-independent) and the prompt is preserved.
	got = withContextHint("hello", "", true)
	if strings.Contains(got, "[Context:") {
		t.Errorf("no-hint first turn should not have a context marker: %q", got)
	}
	if !strings.Contains(got, "corral-question") || !strings.HasSuffix(got, "hello") {
		t.Errorf("no-hint first turn should carry guidance + the prompt: %q", got)
	}
}
