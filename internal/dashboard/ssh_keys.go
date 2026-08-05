package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/config"
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

// handleSSHKeysSelect persists the project's SSH-key EXTRAS (the checked,
// non-global keys from the Config picker) and, if the resolved key set changed,
// RESETS the live scoped agent's loaded identities.
//
// Why reset: the Load-keys PTY resolves keys from saved config, so without
// saving first, deselecting a key in the picker had no effect — the load kept
// prompting for the just-deselected key. And even after saving, a key that was
// ALREADY loaded (e.g. you forgot its passphrase and want to swap it out) would
// linger in the persistent agent. Clearing the agent's identities on a changed
// selection makes "deselect → Load keys" behave: the next load starts clean and
// only prompts for what's actually selected now. The Keychain is untouched, so
// still-selected keys reload silently.
func (d *dashboardServer) handleSSHKeysSelect(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Keys []string `json:"ssh_keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		http.Error(w, "failed to read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	before := cfg.ResolveSSHKeys()
	cfg.SSHKeys = body.Keys
	if err := config.WriteConfig(projectDirForWorkspace(workspace), cfg); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	after := cfg.ResolveSSHKeys()

	// If the resolved set changed at all, reset the live agent so no deselected /
	// stale key survives to be re-prompted. Only touch a live agent (never spawn).
	if !sameStringSet(before, after) {
		if _, live := sshagent.Probe(ProjectID(workspace)); live {
			if ag, aerr := sshagent.Ensure(ProjectID(workspace), after); aerr == nil && ag != nil {
				ag.RemoveAll()
			}
		}
	}
	writeFilesJSON(w, map[string]any{"ok": true, "keys": after})
}

// sameStringSet reports whether a and b contain the same elements (order-blind).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
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
