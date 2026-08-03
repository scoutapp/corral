package dashboard

import (
	"net/http"
	"os/exec"

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
		// side effects), only reports on an already-live one.
		if count, loaded := sshagent.Probe(ProjectID(workspace)); loaded {
			st.Loaded = true
			st.Count = count
		}
	}
	writeFilesJSON(w, st)
}

// handleSSHKeysLoadWS bridges a browser terminal to `ssh-add <keys>` for the
// project's scoped agent, so the user types passphrases in a real PTY. On
// success the agent holds the keys and a subsequent start adopts it.
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

	argv, env := agent.LoadKeysCommand()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	d.bridgePTY(w, r, cmd)
}
