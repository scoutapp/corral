package dashboard

import (
	"strings"
	"testing"
)

func TestWithContextHint(t *testing.T) {
	hint := "The user is viewing repo acme/widget."

	// First turn with a hint: prepended.
	got := withContextHint("what's broken?", hint, true)
	if !strings.HasPrefix(got, "[Context: "+hint+"]") || !strings.Contains(got, "what's broken?") {
		t.Errorf("first-turn hint not prepended: %q", got)
	}

	// Later turns: no hint (already carried via --resume).
	if got := withContextHint("and this one?", hint, false); got != "and this one?" {
		t.Errorf("later turn should not get the hint, got %q", got)
	}

	// No hint (project chat / no context): prompt unchanged even on first turn.
	if got := withContextHint("hello", "", true); got != "hello" {
		t.Errorf("empty hint should leave prompt unchanged, got %q", got)
	}
}
