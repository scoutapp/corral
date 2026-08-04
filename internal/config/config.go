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

	// SSHKeys is the per-project list of private-key paths loaded into this
	// project's scoped ssh-agent (see internal/ssh). When set, it REPLACES the
	// global default (~/.sandclaude/ssh-keys.json) for this project; when unset
	// (nil), the project inherits the global default. An explicit empty list
	// (non-nil, len 0) means "no keys" — no agent is started. Paths may use ~ and
	// are resolved against ~/.ssh when not absolute. See ResolveSSHKeys.
	//
	// No omitempty: an explicit empty list must round-trip as `[]` ("no keys,
	// don't inherit global"), distinct from an absent field (nil = "inherit
	// global"). omitempty would collapse both to absent. On unmarshal, a missing
	// field yields nil and `[]` yields a non-nil empty slice — the distinction we
	// rely on in ResolveSSHKeys.
	SSHKeys []string `json:"ssh_keys"`

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
// this project, applying the global-default + per-project-override policy:
//   - c.SSHKeys nil (field absent)        → inherit the global default
//   - c.SSHKeys non-nil (incl. empty [])  → use it verbatim, replacing the global
//
// Each entry is expanded: ~ → home, and a bare name/relative path → ~/.ssh/<p>.
// The result is deduplicated (stable order) so a key listed in both layers — or
// twice — is only loaded once.
func (c *ProjectConfig) ResolveSSHKeys() []string {
	raw := c.SSHKeys
	if raw == nil {
		raw = globalSSHKeys()
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		abs := ExpandSSHKeyPath(p)
		if abs == "" || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
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
