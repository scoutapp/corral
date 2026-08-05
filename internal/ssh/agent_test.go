package ssh

import (
	"os"
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

// StopAll with no agents root is a no-op returning 0.
func TestStopAll_NoRoot(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", filepath.Join(t.TempDir(), ".sandclaude"))
	if n := StopAll(); n != 0 {
		t.Fatalf("StopAll with no agents root = %d, want 0", n)
	}
}

// StopAll counts each project dir that has a socket file and removes the whole
// agents root afterward. (No real ssh-agent is listening on these sockets, so
// stopAt just fails to connect and removes the file — which is the teardown we
// want; we only assert the count and that the root is gone.)
func TestStopAll_RemovesRootAndCounts(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".sandclaude")
	t.Setenv("SANDCLAUDE_HOME", home)
	root := AgentsRoot()
	// two project dirs with a socket, one without (should not be counted), plus a
	// stray file at the root (should be ignored — not a dir).
	for _, p := range []string{"proj1", "proj2"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0711); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p, "agent.sock"), nil, 0666); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	if n := StopAll(); n != 2 {
		t.Errorf("StopAll counted %d sockets, want 2", n)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("agents root still exists after StopAll: %v", err)
	}
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
