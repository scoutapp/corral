package dashboard

import (
	"os"
	"testing"

	"github.com/scoutapp/corral/internal/session"
)

// The interactive Claude terminal must live on its OWN tmux session, distinct
// from both the container dev session and the plain host shell — otherwise
// attaching one surface would show another's pane. Guard the "-claude" suffix
// and its separation from the "-host" shell session.
func TestClaudeShellSessionIsDistinct(t *testing.T) {
	const ws = "/home/op/projects/acme"

	claude := claudeShellSession(ws)
	host := hostShellSession(ws)
	dev := session.TmuxSessionNameForWorkspace(ws)

	if claude == host {
		t.Fatalf("claude session %q collides with host shell session", claude)
	}
	if claude == dev {
		t.Fatalf("claude session %q collides with dev session", claude)
	}
	if want := dev + "-claude"; claude != want {
		t.Fatalf("claude session = %q, want %q", claude, want)
	}
	// Stable across calls (used as a lookup key on every attach).
	if again := claudeShellSession(ws); again != claude {
		t.Fatalf("claude session not stable: %q then %q", claude, again)
	}
}

// The terminal is behind a flag until it replaces the ChatDock. Confirm the gate
// reads the env var and defaults off.
func TestHostClaudeTerminalGate(t *testing.T) {
	t.Setenv("CORRAL_HOST_CLAUDE_TERMINAL", "")
	if hostClaudeTerminalEnabled() {
		t.Fatal("expected disabled when env unset")
	}
	t.Setenv("CORRAL_HOST_CLAUDE_TERMINAL", "1")
	if !hostClaudeTerminalEnabled() {
		t.Fatal("expected enabled when env is 1")
	}
	t.Setenv("CORRAL_HOST_CLAUDE_TERMINAL", "true")
	if hostClaudeTerminalEnabled() {
		t.Fatal("only the literal \"1\" should enable the flag")
	}
	_ = os.Unsetenv("CORRAL_HOST_CLAUDE_TERMINAL")
}
