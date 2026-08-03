package ssh

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentPID(t *testing.T) {
	cases := map[string]int{
		"SSH_AUTH_SOCK=/tmp/x; export SSH_AUTH_SOCK;\nSSH_AGENT_PID=12345; export SSH_AGENT_PID;\necho Agent pid 12345;\n": 12345,
		"SSH_AGENT_PID=7; export SSH_AGENT_PID;": 7,
		"no pid here":                            0,
		"":                                       0,
		"SSH_AGENT_PID=; export SSH_AGENT_PID;":  0,
	}
	for in, want := range cases {
		if got := parseAgentPID(in); got != want {
			t.Errorf("parseAgentPID(%q) = %d, want %d", in, got, want)
		}
	}
}

// Ensure with an empty key list is a no-op: no agent, no error, no socket.
func TestEnsure_NoKeysIsNoop(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", filepath.Join(t.TempDir(), ".sandclaude"))
	a, err := Ensure("abc123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != nil {
		t.Fatalf("expected nil agent for empty key list, got %+v", a)
	}
}

// Stop on a nil Agent is safe.
func TestStop_NilSafe(t *testing.T) {
	var a *Agent
	a.Stop() // must not panic
}

// The socket path must stay under the macOS Unix-socket limit for a normal home.
func TestSocketPath_WithinLimit(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", "/Users/someuser/.sandclaude")
	dir := agentDir("0123456789ab")
	sock := filepath.Join(dir, "agent.sock")
	if len(sock) > maxUnixSocketPath {
		t.Errorf("socket path %q is %d chars, over the %d limit", sock, len(sock), maxUnixSocketPath)
	}
	if !strings.HasSuffix(sock, "agent.sock") {
		t.Errorf("unexpected socket path %q", sock)
	}
}
