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
