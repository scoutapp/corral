package dashboard

import (
	"encoding/json"
	"fmt"
	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/session"
	sshagent "github.com/jackrothrock/sandclaude/internal/ssh"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// ----------------------------------------------------------------------------
// Dashboard config control plane (write side, Layer 5).
//
// POST /p/<id>/config/diff  -> preview: what would change vs current config
// POST /p/<id>/config/apply -> write + minimal-impact reload
//
// Apply runs the SAME sandclaude CLI subcommands the user would run by hand, with
// the process Dir set to the target workspace. This gives exact CLI/dashboard
// parity (one code path), avoids a process-global os.Chdir in a concurrent
// server, and keeps the minimal-reload logic (applyProxyConfig) in one place.
//
// Reload scope is per-change, per the project's rule:
//   credentials      -> mitmweb only (addon watches the file); no restart
//   monitor / ports  -> SIGHUP the allowlist-proxy only; no restart
//   allowlist        -> re-encrypt + SIGHUP the allowlist-proxy; no restart
//   dind/ports/proxy -> require a project restart (offered as a separate button)
// ----------------------------------------------------------------------------

// configEdit is the editable payload the browser POSTs. Only fields present are
// considered; credentials carry values only on add/update (never read back).
type configEdit struct {
	AllowedHosts *[]string `json:"allowed_hosts,omitempty"`
	MonitorHosts *[]string `json:"monitor_hosts,omitempty"`
	MitmPreset   *string   `json:"mitm_preset,omitempty"` // minimal|all|none|custom
	MitmPorts    *[]string `json:"mitm_ports,omitempty"`

	// Credential mutations, explicit so a masked read-back is never mistaken for
	// an edit: set replaces/adds, unset removes.
	SetCreds   []credSet `json:"set_creds,omitempty"`
	UnsetCreds []string  `json:"unset_creds,omitempty"`

	// Restart-required fields (applied only on an explicit restart).
	ProxyEnabled        *bool     `json:"proxy_enabled,omitempty"`
	PassthroughFirewall *bool     `json:"passthrough_firewall,omitempty"`
	DindEnabled         *bool     `json:"dind_enabled,omitempty"`
	DindPorts           *[]string `json:"dind_ports,omitempty"`
	LaunchTmux          *bool     `json:"launch_tmux,omitempty"`
	SeccompMode         *string   `json:"seccomp_mode,omitempty"`

	// SSH scoped-agent EXTRA key list (restart-required — baked at container
	// start). Union model: these are added on top of the global default; setting
	// [] just means "no project extras" (global still loads). Absent = no edit.
	SSHKeys *[]string `json:"ssh_keys,omitempty"`
}

type credSet struct {
	Host  string `json:"host"`
	Kind  string `json:"kind"` // header | url_param
	Name  string `json:"name"`
	Value string `json:"value"`
}

// diffEntry is one line of the review-before-apply preview.
type diffEntry struct {
	Field   string `json:"field"`
	Change  string `json:"change"` // "+ x", "- y", "~ z"
	Restart bool   `json:"restart"`
}

func (d *dashboardServer) handleConfigDiff(w http.ResponseWriter, r *http.Request, id string) {
	workspace, edit, cur, ok := d.loadEditContext(w, r, id)
	if !ok {
		return
	}
	_ = workspace

	entries := computeDiff(edit, cur, readAllowedHostsForWorkspaceOrNil(workspace))
	writeJSON(w, map[string]any{"entries": entries})
}

func (d *dashboardServer) handleConfigApply(w http.ResponseWriter, r *http.Request, id string) {
	workspace, edit, _, ok := d.loadEditContext(w, r, id)
	if !ok {
		return
	}

	results := []string{}
	fail := func(action string, err error) {
		results = append(results, "✗ "+action+": "+err.Error())
	}
	okMsg := func(action string) { results = append(results, "✓ "+action) }

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, "cannot resolve sandclaude binary: "+err.Error(), http.StatusInternalServerError)
		return
	}
	run := func(args ...string) error {
		cmd := exec.Command(exe, args...)
		cmd.Dir = workspace // CLI commands operate on "the project you're in"
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		return nil
	}

	// Allowlist: write the plaintext file, then firewall-reload re-encrypts + SIGHUPs.
	if edit.AllowedHosts != nil {
		if err := writeAllowedHostsForWorkspace(workspace, *edit.AllowedHosts); err != nil {
			fail("allowlist write", err)
		} else if err := run("firewall-reload"); err != nil {
			fail("firewall-reload", err)
		} else {
			okMsg("allowlist updated + proxy reloaded")
		}
	}

	// Mitm capture preset: persist it to config, then resolve to the effective
	// monitor-host list and apply it live via the same clear+add path below.
	if edit.MitmPreset != nil {
		if cfg, cerr := readConfigForWorkspace(workspace); cerr == nil {
			cfg.MitmPreset = *edit.MitmPreset
			if werr := config.WriteConfig(projectDirForWorkspace(workspace), cfg); werr != nil {
				fail("save mitm preset", werr)
			} else {
				resolved := cfg.ResolveMonitorHosts()
				edit.MonitorHosts = &resolved // drive the live apply below
				okMsg("mitm capture preset: " + *edit.MitmPreset)
			}
		} else {
			fail("read config for preset", cerr)
		}
	}

	// Monitor-list: replace via clear + adds (keeps the CLI as the single writer).
	if edit.MonitorHosts != nil {
		if err := run("monitor", "clear"); err != nil {
			fail("monitor clear", err)
		} else {
			allOK := true
			for _, h := range *edit.MonitorHosts {
				if err := run("monitor", "add", h); err != nil {
					fail("monitor add "+h, err)
					allOK = false
				}
			}
			if allOK {
				okMsg("monitor-list updated + proxy reloaded")
			}
		}
	}

	// Mitm ports: reset then add the desired set.
	if edit.MitmPorts != nil {
		if err := run("mitm-ports", "reset"); err != nil {
			fail("mitm-ports reset", err)
		} else {
			allOK := true
			for _, p := range *edit.MitmPorts {
				if p == "80" || p == "443" {
					continue // defaults already present after reset
				}
				if err := run("mitm-ports", "add", p); err != nil {
					fail("mitm-ports add "+p, err)
					allOK = false
				}
			}
			// If the desired set omits a default port, remove it.
			for _, def := range []string{"80", "443"} {
				if !containsStr(*edit.MitmPorts, def) {
					if err := run("mitm-ports", "remove", def); err != nil {
						fail("mitm-ports remove "+def, err)
						allOK = false
					}
				}
			}
			if allOK {
				okMsg("mitm-ports updated + proxy reloaded")
			}
		}
	}

	// Credentials: set/unset via CLI (writes project creds file; mitmweb auto-reloads).
	for _, c := range edit.SetCreds {
		if err := run("set-cred", c.Host, c.Kind, c.Name, c.Value); err != nil {
			fail("set-cred "+c.Host, err)
		} else {
			okMsg("credential set: " + c.Host)
		}
	}
	for _, h := range edit.UnsetCreds {
		if err := run("unset-cred", h); err != nil {
			fail("unset-cred "+h, err)
		} else {
			okMsg("credential removed: " + h)
		}
	}

	writeJSON(w, map[string]any{"results": results})
}

// handleConfigRestart tears down and restarts the project's container to apply
// restart-required settings. This interrupts any running session in that project
// — the browser confirms first (see config-apply.js).
func (d *dashboardServer) handleConfigRestart(w http.ResponseWriter, r *http.Request, id string) {
	workspace, edit, _, ok := d.loadEditContext(w, r, id)
	if !ok {
		return
	}

	// Persist restart-required edits to config.json before restarting so the new
	// container picks them up. These aren't hot-reloadable by definition.
	if err := applyRestartFields(workspace, edit); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// SSH pre-load gate: the relaunched `sandclaude dev` runs detached (no TTY), so
	// if this project has ssh keys configured but not loaded into its scoped agent,
	// the child would fail on the passphrase prompt and the container would never
	// come back. Detect that BEFORE tearing anything down, and tell the browser to
	// run the Load-keys PTY flow first, then retry the restart (design: pre-load,
	// then start). A running container is left untouched in this case.
	if keys := resolveProjectSSHKeys(workspace); len(keys) > 0 {
		if _, loaded := sshagent.Probe(ProjectID(workspace)); !loaded {
			// Try the macOS Keychain first (silent load if the passphrase was stored
			// before); only demand an interactive load if that didn't work.
			if ag, aerr := sshagent.Ensure(ProjectID(workspace), keys); aerr == nil && ag != nil && ag.TryLoadFromKeychain() {
				// loaded from keychain — proceed with the restart.
			} else {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{
					"ssh_keys_pending": true,
					"results":          []string{"ssh keys need loading before restart — load them, then restart again"},
				})
				return
			}
		}
	}

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Tear down BOTH the container and the stale tmux session, then relaunch `dev`.
	//
	// Two bugs this fixes:
	//   1. `docker kill` removes the --rm container but leaves the detached tmux
	//      session `sandclaude_<name>` alive with a dead pane (remain-on-exit on).
	//      The relaunch's `tmux new-session -s <sameName>` then fails ("duplicate
	//      session") and the container never comes back — and the terminal tab is
	//      still attached to the OLD dead-pane session. So kill the session too.
	//   2. The relaunch must go through a LOGIN SHELL. The dashboard daemon runs
	//      with a stripped PATH (docker/tmux/version-manager dirs missing); a bare
	//      exec.Command(exe, "dev") can't find docker/tmux and silently fails. This
	//      mirrors handleStartProject, which already learned this.
	container := session.ContainerNameForWorkspace(workspace)
	tmuxSession := session.TmuxSessionNameForWorkspace(workspace)
	// `rm -f` (not `kill`): kill on a --rm container triggers ASYNC removal, so an
	// immediate relaunch races it and `docker run --name <same>` fails with "name
	// already in use". rm -f stops AND removes synchronously, so the name is free
	// when we relaunch. Then wait briefly for the name to actually clear.
	_ = exec.Command("docker", "rm", "-f", container).Run()           // best-effort; may already be gone
	_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run() // best-effort; may not exist
	for i := 0; i < 20; i++ {
		if !session.DockerContainerRunning(container) {
			// also ensure the name isn't held by a stopped-but-not-removed container
			if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=^/"+container+"$").Output(); len(out) == 0 {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	startCmd := exec.Command(shell, "-lc", `exec "$0" dev`, exe)
	startCmd.Dir = workspace
	if err := startCmd.Start(); err != nil {
		http.Error(w, "restart failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go func() { _ = startCmd.Wait() }() // detach; the status poll shows it come back

	writeJSON(w, map[string]any{"results": []string{"✓ project restarting (container + tmux session killed, `sandclaude dev` relaunched)"}})
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// loadEditContext resolves the workspace, decodes the POSTed edit, and loads the
// current config. Writes an HTTP error and returns ok=false on any failure.
func (d *dashboardServer) loadEditContext(w http.ResponseWriter, r *http.Request, id string) (workspace string, edit configEdit, cur *config.ProjectConfig, ok bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
		http.Error(w, "bad edit payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	cur, err = readConfigForWorkspace(workspace)
	if err != nil {
		http.Error(w, "read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	return workspace, edit, cur, true
}

func computeDiff(edit configEdit, cur *config.ProjectConfig, curAllowed []string) []diffEntry {
	var out []diffEntry

	if edit.AllowedHosts != nil {
		out = append(out, listDiff("allowed host", curAllowed, *edit.AllowedHosts, false)...)
	}
	if edit.MonitorHosts != nil {
		out = append(out, listDiff("monitor", cur.MonitorHosts, *edit.MonitorHosts, false)...)
	}
	if edit.MitmPorts != nil {
		out = append(out, listDiff("mitm port", cur.MitmPortsOrDefault(), *edit.MitmPorts, false)...)
	}
	for _, c := range edit.SetCreds {
		out = append(out, diffEntry{Field: "credential", Change: "~ " + c.Host + " (" + c.kindLabel() + ")"})
	}
	for _, h := range edit.UnsetCreds {
		out = append(out, diffEntry{Field: "credential", Change: "- " + h})
	}

	// Restart-required
	if edit.ProxyEnabled != nil && *edit.ProxyEnabled != cur.ProxyEnabled {
		out = append(out, diffEntry{Field: "proxy_enabled", Change: fmt.Sprintf("~ %v → %v", cur.ProxyEnabled, *edit.ProxyEnabled), Restart: true})
	}
	if edit.PassthroughFirewall != nil && *edit.PassthroughFirewall != cur.PassthroughFirewall {
		out = append(out, diffEntry{Field: "passthrough_firewall", Change: fmt.Sprintf("~ %v → %v", cur.PassthroughFirewall, *edit.PassthroughFirewall), Restart: true})
	}
	if edit.DindEnabled != nil && *edit.DindEnabled != cur.DindEnabled {
		out = append(out, diffEntry{Field: "dind_enabled", Change: fmt.Sprintf("~ %v → %v", cur.DindEnabled, *edit.DindEnabled), Restart: true})
	}
	if edit.DindPorts != nil {
		out = append(out, listDiff("published port", cur.DindPorts, *edit.DindPorts, true)...)
	}
	if edit.LaunchTmux != nil && *edit.LaunchTmux != cur.LaunchTmux {
		out = append(out, diffEntry{Field: "launch_tmux", Change: fmt.Sprintf("~ %v → %v", cur.LaunchTmux, *edit.LaunchTmux), Restart: true})
	}
	if edit.SeccompMode != nil && *edit.SeccompMode != cur.SeccompMode {
		out = append(out, diffEntry{Field: "seccomp_mode", Change: fmt.Sprintf("~ %q → %q", cur.SeccompMode, *edit.SeccompMode), Restart: true})
	}
	if edit.SSHKeys != nil {
		out = append(out, listDiff("ssh key (project extra)", cur.SSHKeys, *edit.SSHKeys, true)...)
	}
	return out
}

// listDiff emits + / - entries for items added/removed between old and new.
func listDiff(field string, oldList, newList []string, restart bool) []diffEntry {
	oldSet, newSet := toSet(oldList), toSet(newList)
	var out []diffEntry
	added, removed := []string{}, []string{}
	for x := range newSet {
		if _, ok := oldSet[x]; !ok {
			added = append(added, x)
		}
	}
	for x := range oldSet {
		if _, ok := newSet[x]; !ok {
			removed = append(removed, x)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	for _, x := range added {
		out = append(out, diffEntry{Field: field, Change: "+ " + x, Restart: restart})
	}
	for _, x := range removed {
		out = append(out, diffEntry{Field: field, Change: "- " + x, Restart: restart})
	}
	return out
}

func applyRestartFields(workspace string, edit configEdit) error {
	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		return err
	}
	if edit.ProxyEnabled != nil {
		cfg.ProxyEnabled = *edit.ProxyEnabled
	}
	if edit.PassthroughFirewall != nil {
		cfg.PassthroughFirewall = *edit.PassthroughFirewall
	}
	if edit.DindEnabled != nil {
		cfg.DindEnabled = *edit.DindEnabled
	}
	if edit.DindPorts != nil {
		cfg.DindPorts = *edit.DindPorts
	}
	if edit.LaunchTmux != nil {
		cfg.LaunchTmux = *edit.LaunchTmux
	}
	if edit.SeccompMode != nil {
		cfg.SeccompMode = *edit.SeccompMode
	}
	// SSH project extras: set verbatim (union with global happens at resolve time).
	if edit.SSHKeys != nil {
		cfg.SSHKeys = *edit.SSHKeys
	}
	return config.WriteConfig(projectDirForWorkspace(workspace), cfg)
}

// writeAllowedHostsForWorkspace writes the plaintext allowlist (one host per
// line). firewall-reload (run separately) re-encrypts + reloads from it.
func writeAllowedHostsForWorkspace(workspace string, hosts []string) error {
	path := sandclaudeDirForWorkspace(workspace) + "/allowed-domains.txt"
	content := strings.Join(hosts, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func readAllowedHostsForWorkspaceOrNil(workspace string) []string {
	return readAllowedHostsForWorkspace(workspace)
}

func (c credSet) kindLabel() string { return c.Kind + ": " + c.Name }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func toSet(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[x] = struct{}{}
	}
	return m
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
