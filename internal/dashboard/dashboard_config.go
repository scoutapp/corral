package dashboard

import (
	"bufio"
	"encoding/json"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
	"github.com/scoutapp/corral/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ----------------------------------------------------------------------------
// Dashboard config control plane (read side).
//
// GET /p/<id>/config returns the project's full configuration for the Config
// tab, split into two zones by how a change takes effect:
//   - live:    allowlist, monitor-list, mitm-ports, credentials — hot-reloadable
//              without restarting the container (Apply just writes + reloads).
//   - restart: workspace, proxy_enabled, dind_enabled, dind_ports, launch_tmux —
//              baked into `docker run`, so changing them needs a project restart.
//
// Credential VALUES are never sent to the browser — only the host + which
// header/param carries the secret, plus a masked marker. The write side (Layer 5)
// only ever sets values, never echoes them back.
// ----------------------------------------------------------------------------

type credView struct {
	Host   string `json:"host"`
	Kind   string `json:"kind"`   // "header" or "url_param"
	Name   string `json:"name"`   // the header/param name (not secret)
	Masked string `json:"masked"` // always "********" — the value is never exposed
}

type configView struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`

	// Live zone
	AllowedHosts []string   `json:"allowed_hosts"`
	MonitorHosts []string   `json:"monitor_hosts"` // the custom list (empty for non-custom presets)
	MonitorAll   bool       `json:"monitor_all"`
	MitmPreset   string     `json:"mitm_preset"` // minimal|all|none|custom
	MitmPorts    []string   `json:"mitm_ports"`
	Credentials  []credView `json:"credentials"`

	// Restart-required zone
	ProxyEnabled        bool                 `json:"proxy_enabled"`
	PassthroughFirewall bool                 `json:"passthrough_firewall"`
	DindEnabled         bool                 `json:"dind_enabled"`
	DindPorts           []string             `json:"dind_ports"`
	DindCache           *config.DindCacheRef `json:"dind_cache,omitempty"`
	LaunchTmux          bool                 `json:"launch_tmux"`
	SeccompMode         string               `json:"seccomp_mode"` // "" default | unconfined | <path>

	// SSH scoped-agent (union model). The effective set is global ∪ project extras.
	//   SSHKeys          — this project's EXTRA key paths (added on top of global)
	//   SSHKeysGlobal     — the global default paths (always-on; shown pre-checked
	//                       + locked in the picker — managed in global settings)
	//   SSHKeysEffective  — the resolved union actually loaded (absolute paths)
	SSHKeys          []string `json:"ssh_keys"`
	SSHKeysGlobal    []string `json:"ssh_keys_global"`
	SSHKeysEffective []string `json:"ssh_keys_effective"`

	// Live status, so the panel can show whether a reload will actually reach a
	// running proxy or is just being saved for next start.
	ContainerUp bool `json:"container_up"`

	// Source: the PR/issue this project was spawned from (back-link), or nil.
	Source *config.ProjectSource `json:"source,omitempty"`

	// RepoID is the project's primary repo id (from Source), when known. The
	// Config tab uses it to offer a repo-scoped DinD cache ("Save as repo cache",
	// named repo-<id>). Empty when the project isn't repo-derived.
	RepoID string `json:"repo_id,omitempty"`
}

func (d *dashboardServer) handleConfigRead(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	cfg, err := readConfigForWorkspace(workspace)
	if err != nil {
		http.Error(w, "failed to read project config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	view := configView{
		ID:                  id,
		Workspace:           workspace,
		MonitorHosts:        cfg.MonitorHosts,
		MonitorAll:          len(cfg.MonitorHosts) == 0,
		MitmPreset:          cfg.MitmPresetOrDefault(),
		MitmPorts:           cfg.MitmPortsOrDefault(),
		ProxyEnabled:        cfg.ProxyEnabled,
		PassthroughFirewall: cfg.PassthroughFirewall,
		DindEnabled:         cfg.DindEnabled,
		DindPorts:           cfg.DindPorts,
		DindCache:           cfg.DindCache,
		LaunchTmux:          cfg.LaunchTmux,
		SeccompMode:         cfg.SeccompMode,
		ContainerUp:         session.DockerContainerRunning(session.ContainerNameForWorkspace(workspace)),

		SSHKeys:          cfg.SSHKeys,
		SSHKeysGlobal:    config.GlobalSSHKeys(),
		SSHKeysEffective: cfg.ResolveSSHKeys(),
		Source:           cfg.Source,
	}
	if cfg.Source != nil {
		view.RepoID = cfg.Source.RepoID
	}

	view.AllowedHosts = readAllowedHostsForWorkspace(workspace)
	view.Credentials = readMaskedCredsForWorkspace(workspace)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ----------------------------------------------------------------------------
// Workspace-parameterized readers. The dashboard is host-wide and inspects other
// projects, so (like projectDirForWorkspace/logsDirForWorkspace) these resolve
// paths from a workspace argument rather than os.Getwd().
// ----------------------------------------------------------------------------

// corralDirForWorkspace is <workspace>/.corral (the plaintext allowlist,
// logs/, and project/ live under here). Mirrors the cwd-based config.CorralDir().
func corralDirForWorkspace(workspace string) string {
	return filepath.Join(workspace, ".corral")
}

func readConfigForWorkspace(workspace string) (*config.ProjectConfig, error) {
	return config.ReadConfig(projectDirForWorkspace(workspace))
}

// readAllowedHostsForWorkspace reads the plaintext allowlist (the human-editable
// source; the .enc is derived from it). Returns nil if absent.
func readAllowedHostsForWorkspace(workspace string) []string {
	path := filepath.Join(corralDirForWorkspace(workspace), "allowed-domains.txt")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var hosts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hosts = append(hosts, line)
	}
	sort.Strings(hosts)
	return hosts
}

// readMaskedCredsForWorkspace returns credential metadata with values masked —
// host + which header/param carries the secret, never the secret itself. Reads
// the project-scoped credentials file (the one the dashboard write side edits).
func readMaskedCredsForWorkspace(workspace string) []credView {
	path := filepath.Join(projectDirForWorkspace(workspace), "proxy-credentials.json")
	credsMap, err := creds.LoadCredsMap(path)
	if err != nil {
		return nil
	}

	out := make([]credView, 0, len(credsMap))
	for host, entry := range credsMap {
		cv := credView{Host: host, Masked: "********"}
		if v, ok := entry["header"]; ok {
			cv.Kind, cv.Name = "header", v
		} else if v, ok := entry["url_param"]; ok {
			cv.Kind, cv.Name = "url_param", v
		}
		out = append(out, cv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}
