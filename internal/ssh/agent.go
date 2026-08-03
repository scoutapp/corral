// Package ssh manages per-project scoped ssh-agents: a fresh ssh-agent holding
// ONLY the keys chosen for a project, exposed to the container as a bind-mounted
// socket. The container can USE the keys (signing oracle) but never reads the
// key bytes — there is no key file mounted, only the agent socket.
//
// Why a scoped agent instead of forwarding the host's real agent:
//   - Scoping: the host's real agent may hold keys we don't want the container to
//     use. A scoped agent holds only the project's chosen keys.
//   - No byte leak: the ssh-agent protocol has no "export key" operation, so even
//     a compromised/escaped container can sign with the key but cannot copy it.
//
// Lifetime is tied to the container: Start() creates the agent + socket and the
// caller loads keys into it (interactively, via the foreground shell or the
// dashboard host-terminal PTY, since keys are passphrase-protected); Stop() kills
// the agent and removes the socket. A restart re-runs the whole flow (keys are
// held only in the agent's memory and are re-prompted) — see docs/security.md.
//
// macOS-only reasoning: the socket lives under ~/.sandclaude (a Docker-Desktop
// shared path) so Docker Desktop's virtiofs proxies the Unix-socket connection
// macOS -> VM -> container. On Linux this design still works but the cross-project
// residual risk differs (privileged escape hits the real host, not a throwaway
// VM) — deferred.
package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/config"
)

// ContainerSocketPath is where the scoped-agent socket is mounted inside the
// container; the container's SSH_AUTH_SOCK points here.
const ContainerSocketPath = "/ssh-agent.sock"

// maxUnixSocketPath is the practical sun_path limit on macOS/BSD (104). A socket
// path at or beyond this fails bind with a cryptic "path too long" error, so we
// check up front and surface a clear message instead.
const maxUnixSocketPath = 103

// Agent is a running scoped ssh-agent for one project.
type Agent struct {
	// SocketPath is the host path of the agent's Unix socket (bind-mount source).
	SocketPath string
	// Keys are the absolute private-key paths this agent is meant to hold (the
	// resolved, deduplicated list). The caller loads them via LoadKeysCommand.
	Keys []string

	pid int // ssh-agent process pid, for Stop()
	dir string
}

// AgentsRoot is ~/.sandclaude/agents — the parent of all per-project agent dirs.
func AgentsRoot() string {
	return filepath.Join(config.SandclaudeHome(), "agents")
}

// agentDir is the per-project directory holding this project's socket. projectID
// is expected to be a short, filesystem-safe token (the dashboard's 12-hex
// ProjectID); we keep the leaf minimal to stay under the socket-path limit.
func agentDir(projectID string) string {
	return filepath.Join(AgentsRoot(), projectID)
}

// socketPathFor returns the (dir, socket) for a project, validating the socket
// path against the macOS Unix-socket length limit.
func socketPathFor(projectID string) (dir, sock string, err error) {
	dir = agentDir(projectID)
	sock = filepath.Join(dir, "agent.sock")
	if len(sock) > maxUnixSocketPath {
		return "", "", fmt.Errorf("ssh-agent socket path too long (%d > %d): %s\n"+
			"set SANDCLAUDE_HOME to a shorter directory", len(sock), maxUnixSocketPath, sock)
	}
	return dir, sock, nil
}

// Ensure returns a scoped ssh-agent for the project, ADOPTING an already-running
// one when its socket is live (so a dashboard pre-load survives the subsequent
// `sandclaude dev`), or creating a fresh agent otherwise. It never tears down a
// live agent — that's Stop()'s job. It does NOT load keys (loading is interactive
// via a PTY the caller owns).
//
// Returns nil, nil when keys is empty: no keys chosen means no agent (and the
// caller should not mount a socket or set SSH_AUTH_SOCK).
func Ensure(projectID string, keys []string) (*Agent, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	dir, sock, err := socketPathFor(projectID)
	if err != nil {
		return nil, err
	}

	// Adopt a live agent if the socket exists AND responds (a bind-mounted socket
	// from a previous run whose agent died leaves a stale file; probe before trust).
	if _, statErr := os.Stat(sock); statErr == nil {
		if socketResponds(sock) {
			return &Agent{SocketPath: sock, Keys: append([]string(nil), keys...), dir: dir}, nil
		}
		// Stale socket (agent gone). Clear it so `ssh-agent -a` can rebind.
		stopAt(sock)
		_ = os.RemoveAll(dir)
	}

	// The socket is bind-mounted into the container, whose `claude` user has a
	// DIFFERENT uid than the host user that owns it. ssh-agent creates the dir-
	// traversal + socket as owner-only (0700 dir / 0600 socket), so the container
	// user gets "Permission denied" connecting to the agent. Open up both so the
	// container can reach it: 0711 dir (traverse-only, contents not listable) and
	// 0666 socket below. This only grants the ability to USE the loaded keys (the
	// signing-oracle we intend to give the container); no key bytes are exposed,
	// and it all lives under the user-private ~/.sandclaude.
	if err := os.MkdirAll(dir, 0711); err != nil {
		return nil, fmt.Errorf("create agent dir %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0711) // MkdirAll honors umask; force it

	// `ssh-agent -a <sock>` binds our own socket (vs. letting ssh-agent pick one),
	// so the path is known and lives under the Docker-shared dir. It prints
	// SSH_AUTH_SOCK=… and SSH_AGENT_PID=… on stdout, which we parse for the pid.
	out, err := exec.Command("ssh-agent", "-a", sock).Output()
	if err != nil {
		return nil, fmt.Errorf("start ssh-agent: %w", err)
	}
	pid := parseAgentPID(string(out))
	if pid == 0 {
		stopAt(sock)
		return nil, fmt.Errorf("could not parse ssh-agent pid from: %q", string(out))
	}

	// Make the socket connectable by the container user (see the dir note above).
	if err := os.Chmod(sock, 0666); err != nil {
		config.Debugf("warning: could not chmod agent socket %s: %v", sock, err)
	}

	return &Agent{SocketPath: sock, Keys: append([]string(nil), keys...), pid: pid, dir: dir}, nil
}

// Probe reports, WITHOUT creating anything, how many identities the project's
// scoped agent currently holds. Returns (0, false) when no live agent exists —
// used by the dashboard status endpoint so a mere status poll never spawns an
// agent. loaded is true iff a live agent holds ≥1 identity.
func Probe(projectID string) (count int, loaded bool) {
	_, sock, err := socketPathFor(projectID)
	if err != nil {
		return 0, false
	}
	if _, statErr := os.Stat(sock); statErr != nil {
		return 0, false
	}
	if !socketResponds(sock) {
		return 0, false
	}
	a := &Agent{SocketPath: sock}
	fps, _ := a.LoadedFingerprints()
	return len(fps), len(fps) > 0
}

// socketResponds reports whether a live ssh-agent is answering on sock (via a
// cheap `ssh-add -l`, which returns 0 with keys or 1 for "no identities" — both
// mean the agent is alive; only a connection failure means it's dead/stale).
func socketResponds(sock string) bool {
	cmd := exec.Command("ssh-add", "-l")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true // exit 0: agent alive, has identities
	}
	// exit 1 with "no identities" = alive but empty; any "connect"/"communication"
	// failure = dead. Distinguish by message.
	s := string(out)
	if strings.Contains(s, "no identities") {
		return true
	}
	return false
}

// LoadKeysCommand returns the argv for loading this agent's keys, to be run in a
// PTY the caller owns (foreground shell or dashboard host-terminal) so the user
// can type each key's passphrase. It sets SSH_AUTH_SOCK in the returned env so
// the load targets THIS scoped agent, never the user's real one.
//
// Returns (argv, env). env is a full os.Environ()-style slice with SSH_AUTH_SOCK
// overridden. Missing key files are included as-is; ssh-add reports them clearly.
func (a *Agent) LoadKeysCommand() (argv []string, env []string) {
	argv = append([]string{"ssh-add"}, a.Keys...)
	env = append(os.Environ(), "SSH_AUTH_SOCK="+a.SocketPath)
	return argv, env
}

// LoadedFingerprints returns the fingerprints currently held by the agent (via
// `ssh-add -l`), so the caller can tell whether keys still need loading (e.g. to
// skip a redundant PTY prompt). An agent with no identities yields an empty slice
// and no error.
func (a *Agent) LoadedFingerprints() ([]string, error) {
	cmd := exec.Command("ssh-add", "-l")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+a.SocketPath)
	out, err := cmd.Output()
	if err != nil {
		// ssh-add -l exits nonzero when the agent has no identities (and in a few
		// other states). For our purpose — "does it still need loading?" — any
		// error is safely treated as "nothing loaded". Be lenient.
		return nil, nil
	}
	var fps []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			fps = append(fps, f)
		}
	}
	return fps, nil
}

// Stop kills the agent process and removes its socket + directory. Safe to call
// on a nil Agent (no-op) and idempotent.
func (a *Agent) Stop() {
	if a == nil {
		return
	}
	stopAt(a.SocketPath)
	if a.dir != "" {
		os.RemoveAll(a.dir)
	}
}

// stopAt kills whatever ssh-agent is listening on sock (best effort) by asking
// ssh-agent itself to shut down via that socket. Used both on teardown and to
// clear a stale agent before a fresh Start.
func stopAt(sock string) {
	if sock == "" {
		return
	}
	if _, err := os.Stat(sock); err != nil {
		return
	}
	cmd := exec.Command("ssh-agent", "-k")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	_ = cmd.Run() // best effort; the socket file is removed by the caller
	_ = os.Remove(sock)
}

// parseAgentPID extracts the pid from ssh-agent's Bourne-shell startup output:
//
//	SSH_AUTH_SOCK=/…; export SSH_AUTH_SOCK;
//	SSH_AGENT_PID=12345; export SSH_AGENT_PID;
//	echo Agent pid 12345;
func parseAgentPID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		const marker = "SSH_AGENT_PID="
		if i := strings.Index(line, marker); i >= 0 {
			rest := line[i+len(marker):]
			// value ends at ';'
			if j := strings.IndexByte(rest, ';'); j >= 0 {
				rest = rest[:j]
			}
			pid := 0
			for _, c := range strings.TrimSpace(rest) {
				if c < '0' || c > '9' {
					break
				}
				pid = pid*10 + int(c-'0')
			}
			if pid > 0 {
				return pid
			}
		}
	}
	return 0
}
