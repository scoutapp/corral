package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ProjectConfig struct {
	Workspace    string   `json:"workspace"`
	ProxyEnabled bool     `json:"proxy_enabled,omitempty"`
	DindEnabled  bool     `json:"dind_enabled,omitempty"`
	DindPorts    []string `json:"dind_ports,omitempty"`
	LaunchTmux   bool     `json:"launch_tmux,omitempty"`

	// PassthroughFirewall = "permissive but observed" mode (the saved form of
	// --passthrough-firewall-and-write): proxy + mitm stay ON (HTTP/S inspected,
	// credentials injected), but unknown domains are ALLOWED and logged to
	// allowed-domains.txt instead of blocked, and the iptables egress REJECT is
	// skipped so direct TCP (e.g. git-over-ssh) works. Only meaningful when
	// ProxyEnabled is true. Persisted so a project keeps this mode across starts.
	PassthroughFirewall bool `json:"passthrough_firewall,omitempty"`

	// Selective mitm (see allowlist-proxy). MonitorHosts, when non-empty, is the
	// set of hosts routed through mitmweb for full interception + credential
	// injection; every other allowed host is direct-dialed (still logged, not
	// decrypted). Empty = monitor all allowed hosts (default).
	MonitorHosts []string `json:"monitor_hosts,omitempty"`
	// MitmPorts are the destination ports eligible for mitm; CONNECT to any other
	// port (ssh, SOCKS, …) is direct-dialed. Empty = default 80,443.
	MitmPorts []string `json:"mitm_ports,omitempty"`

	// SeccompMode selects the container's seccomp profile:
	//   "" / "default"    — Docker's default seccomp profile
	//   "unconfined"      — no seccomp filtering (needed for some runtimes, e.g.
	//                       Erlang/BEAM, that make syscalls the default blocks)
	//   "<path>"          — a custom profile JSON on the host
	// Note: with DinD (--privileged) seccomp is already disabled, so this only
	// takes effect when DinD is off.
	SeccompMode string `json:"seccomp_mode,omitempty"`

	// SSHKeys is the per-project list of EXTRA private-key paths, added on top of
	// the global default (~/.sandclaude/ssh-keys.json). The effective set loaded
	// into the scoped ssh-agent is the union global ∪ project (see ResolveSSHKeys)
	// — the project list adds to the global, it does not replace it. Empty/absent
	// = no extras (the global default still loads). Paths may use ~ and are
	// resolved against ~/.ssh when not absolute.
	SSHKeys []string `json:"ssh_keys,omitempty"`

	// MitmPreset is a friendly capture policy that resolves to MonitorHosts:
	//   "minimal" (default) — MITM only Claude + GitHub hosts
	//   "all"               — MITM every allowed host (MonitorHosts empty)
	//   "none"              — MITM nothing
	//   "custom"            — use MonitorHosts as-is (the explicit host list)
	// Empty is treated as the default preset (see MitmPresetOrDefault).
	MitmPreset string `json:"mitm_preset,omitempty"`

	CreatedAt string `json:"created_at"`
}

// MonitorNoneSentinel is a host that never matches a real hostname; used as the
// sole monitor-list entry for the "none" preset so monitorActive is on (turning
// on selective mode) but no real host is ever selected for interception.
const MonitorNoneSentinel = "__mitm_none__"

// The Claude + GitHub host set for the "minimal" preset — the traffic worth
// intercepting for credential injection / inspection, leaving everything else
// allowlisted-but-direct-dialed.
var MinimalMonitorHosts = []string{
	"api.anthropic.com",
	"platform.claude.com",
	"statsig.anthropic.com",
	"claude.ai",
	"api.github.com",
	"github.com",
	"raw.githubusercontent.com",
}

// MitmPresetOrDefault returns the configured preset, defaulting to "minimal".
// A project with an explicit MonitorHosts list but no preset is treated as
// "custom" so upgrades don't silently change existing selective-mitm setups.
func (c *ProjectConfig) MitmPresetOrDefault() string {
	if c.MitmPreset != "" {
		return c.MitmPreset
	}
	if len(c.MonitorHosts) > 0 {
		return "custom"
	}
	return "minimal"
}

// ResolveMonitorHosts turns the preset into the effective monitor-host list the
// proxy consumes. "all" -> empty (proxy interprets empty as monitor-all);
// "none" -> the never-matching sentinel; "minimal" -> the curated set;
// "custom" (or unknown) -> the explicit MonitorHosts.
func (c *ProjectConfig) ResolveMonitorHosts() []string {
	switch c.MitmPresetOrDefault() {
	case "all":
		return nil
	case "none":
		return []string{MonitorNoneSentinel}
	case "minimal":
		return append([]string(nil), MinimalMonitorHosts...)
	default: // custom
		return c.MonitorHosts
	}
}

// MitmPortsOrDefault returns the configured mitm ports, or the 80,443 default
// when unset — centralizing the default so start, apply, and the dashboard agree.
func (c *ProjectConfig) MitmPortsOrDefault() []string {
	if len(c.MitmPorts) == 0 {
		return []string{"80", "443"}
	}
	return c.MitmPorts
}

// GlobalSSHKeysPath is ~/.sandclaude/ssh-keys.json — the cross-project default
// key list, mirroring the global proxy-credentials.json pattern.
func GlobalSSHKeysPath() string {
	return filepath.Join(SandclaudeHome(), "ssh-keys.json")
}

// GlobalSSHKeys reads the global default key list (raw, unexpanded paths).
// Missing file / parse error → nil. Exported so the dashboard can show the
// global set in the picker (pre-checked/locked) and edit it in global settings.
func GlobalSSHKeys() []string { return globalSSHKeys() }

// WriteGlobalSSHKeys persists the global default key list to
// ~/.sandclaude/ssh-keys.json (0600). Paths are stored as given (may be bare
// names / ~-relative); resolution happens at load time. Passing an empty slice
// clears the global default.
func WriteGlobalSSHKeys(keys []string) error {
	if keys == nil {
		keys = []string{}
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SandclaudeHome(), 0700); err != nil {
		return err
	}
	return os.WriteFile(GlobalSSHKeysPath(), data, 0600)
}

// globalSSHKeys reads the global default key list. Missing file / parse error →
// nil (no global default), which is a normal "no keys configured" state, not an
// error we want to fail a container start over.
func globalSSHKeys() []string {
	data, err := os.ReadFile(GlobalSSHKeysPath())
	if err != nil {
		return nil
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil
	}
	return keys
}

// ResolveSSHKeys returns the effective list of ABSOLUTE private-key paths for
// this project as the UNION of the global default and the project's own extras:
//
//	effective = globalSSHKeys() ∪ c.SSHKeys
//
// Global keys always load; the project list ADDS to them (it does not replace).
// This is what "use the global default and add a couple project-specific keys"
// requires. Global entries come first (they're the always-on base), project
// extras after, deduplicated in stable order so a key in both — or listed twice —
// loads once. Each entry is expanded: ~ → home, bare name/relative → ~/.ssh/<p>.
func (c *ProjectConfig) ResolveSSHKeys() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.SSHKeys)+2)
	add := func(list []string) {
		for _, p := range list {
			abs := ExpandSSHKeyPath(p)
			if abs == "" || seen[abs] {
				continue
			}
			seen[abs] = true
			out = append(out, abs)
		}
	}
	add(globalSSHKeys()) // always-on base
	add(c.SSHKeys)       // project extras
	return out
}

// ExpandSSHKeyPath resolves a configured key entry to an absolute path:
//   - "" → ""
//   - "~/x" or "~" → under the home dir
//   - absolute → as-is
//   - anything else (bare name or relative) → under ~/.ssh
//
// It does NOT check existence — callers surface a clear error when loading.
func ExpandSSHKeyPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(home, ".ssh", p)
}

func ReadConfig(projectDir string) (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("config not found — run: sandclaude init")
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w", err)
	}
	return &cfg, nil
}

func WriteConfig(projectDir string, cfg *ProjectConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectDir, "config.json"), data, 0600)
}

// WriteMonitorHostsFile materializes the monitor-list to a newline-separated
// plaintext file (0644 so proxyuser in the container can read the bind-mount).
// The file is the on-disk form the allowlist-proxy's --monitorlist reads.
func WriteMonitorHostsFile(path string, hosts []string) error {
	content := strings.Join(hosts, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}
