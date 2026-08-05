package dashboard

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	sshagent "github.com/jackrothrock/sandclaude/internal/ssh"
)

// ----------------------------------------------------------------------------
// SSH scoped-agent control plane (dashboard side).
//
// The dashboard "start" path runs `sandclaude dev` DETACHED — no TTY — so it
// cannot prompt for a passphrase-protected key. The fix (design: "pre-load, then
// start"): before starting, the browser loads the project's chosen keys into the
// project's scoped ssh-agent through a PTY the dashboard owns (the same
// bridgePTY the host terminal uses), so the passphrase prompt appears in a real
// terminal. `sandclaude dev` then ADOPTS that already-loaded agent (see
// internal/ssh Ensure + container startSSHAgent) instead of re-prompting.
//
//   GET  /p/<id>/sshkeys/status  -> { configured, loaded, keys[], count }
//   WS   /p/<id>/sshkeys/ws      -> PTY running `ssh-add <keys>` for the scoped
//                                   agent; browser xterm shows the prompt.
//
// Loading only ever runs ssh-add against the PROJECT's scoped agent socket
// (SSH_AUTH_SOCK overridden), never the operator's real agent. No passphrase is
// ever sent as a value — it's typed into the PTY and read by ssh-add directly.
// ----------------------------------------------------------------------------

type sshKeysStatus struct {
	Configured bool     `json:"configured"` // any keys resolved for this project
	Loaded     bool     `json:"loaded"`     // scoped agent currently holds identities
	Keys       []string `json:"keys"`       // absolute key paths (not secret)
	Count      int      `json:"count"`      // number of loaded identities
}

// resolveProjectSSHKeys returns the effective key list for a project workspace.
func resolveProjectSSHKeys(workspace string) []string {
	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		return nil
	}
	return cfg.ResolveSSHKeys()
}

// handleSSHKeysAvailable lists the SSH keys found under ~/.ssh (name/type/comment)
// so the picker can render labeled checkboxes instead of asking the user to type
// key paths. Host-wide data (same for every project); the project id only gates
// auth. Public metadata only — never key bytes.
func (d *dashboardServer) handleSSHKeysAvailable(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := lookupWorkspaceByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	writeFilesJSON(w, map[string]any{"keys": sshagent.AvailableKeys()})
}

func (d *dashboardServer) handleSSHKeysStatus(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	keys := resolveProjectSSHKeys(workspace)
	st := sshKeysStatus{Configured: len(keys) > 0, Keys: keys}

	if st.Configured {
		// Probe is read-only: it never spawns an agent (a status poll must have no
		// side effects), only reports on an already-live one. "Loaded" means ALL
		// resolved keys are present, not just ≥1 — otherwise the Config pane shows a
		// green "loaded" while a project-specific key is still missing.
		if count, live := sshagent.Probe(ProjectID(workspace)); live {
			st.Count = count
			st.Loaded = count >= len(keys)
		}
	}
	writeFilesJSON(w, st)
}

// handleSSHKeysLoadWS bridges a browser terminal to a real interactive login
// shell ON THE HOST that runs `ssh-add <keys>` for the project's scoped agent,
// then drops to a prompt. Running it inside `$SHELL -i` (rather than exec'ing
// ssh-add bare) gives the familiar user@host:cwd$ prompt and makes clear this is
// a genuine host shell, not a sandboxed one — the same treatment the container
// shell got. The passphrase is typed into the real PTY and read by ssh-add
// directly; it never becomes a request value. On success the scoped agent holds
// the keys and a subsequent start adopts it.
func (d *dashboardServer) handleSSHKeysLoadWS(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	keys := resolveProjectSSHKeys(workspace)
	if len(keys) == 0 {
		http.Error(w, "no ssh keys configured for this project", http.StatusBadRequest)
		return
	}

	agent, err := sshagent.Ensure(ProjectID(workspace), keys)
	if err != nil {
		http.Error(w, "failed to start scoped ssh-agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if agent == nil {
		http.Error(w, "no ssh keys configured for this project", http.StatusBadRequest)
		return
	}

	// Build the ssh-add invocation as a single shell command. Each key path is
	// single-quoted (paths are host-resolved from config; quoting guards spaces).
	// On macOS add --apple-use-keychain so the passphrase you type ONCE here is
	// stored in the login Keychain and reused silently on later loads (the "type
	// once, reuse" behavior). Apple-specific flag — omitted on Linux.
	sshAddFlags := ""
	if runtime.GOOS == "darwin" {
		sshAddFlags = "--apple-use-keychain "
	}
	var quoted []string
	for _, k := range agent.Keys {
		quoted = append(quoted, shellSingleQuote(k))
	}
	addCmd := "ssh-add " + sshAddFlags + strings.Join(quoted, " ")

	// A short banner (so it's unmistakably a host shell), then run ssh-add, then
	// exec an interactive login shell so the prompt (user@host:cwd$) appears and
	// the user can see the result / retry. `exec` replaces the launcher so there's
	// one clean process. SSH_AUTH_SOCK targets the scoped agent.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	banner := "── sandclaude · HOST shell (NOT sandboxed) · loading SSH keys into the scoped agent for this project ──"
	script := "echo " + shellSingleQuote(banner) + "; " +
		addCmd + "; exec " + shell + " -i"

	cmd := exec.Command(shell, "-lc", script)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+agent.SocketPath)
	if _, serr := os.Stat(workspace); serr == nil {
		cmd.Dir = workspace // land in the project so the prompt's cwd is meaningful
	}
	d.bridgePTY(w, r, cmd)
}

// shellSingleQuote wraps s in single quotes for safe use in a /bin/sh command,
// escaping embedded single quotes via the '\” idiom.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
