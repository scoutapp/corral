package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ----------------------------------------------------------------------------
// Selective-mitm CLI: monitor-list, mitm-ports, credentials, and the unified
// proxy-apply that re-materializes config to the running proxies without a
// restart. These mirror the dashboard control plane (Layers 4-5) so every
// dashboard action has a scriptable CLI equivalent.
//
// Reload granularity is deliberately minimal (see applyProxyConfig): a monitor/
// port change SIGHUPs only the allowlist-proxy; a credential change touches only
// mitmweb (its addon watches the file). Neither restarts the container.
// ----------------------------------------------------------------------------

// cmdMonitor: sandclaude monitor [list|add <host>|remove <host>|clear]
// Manages the monitor-list — the hosts routed through mitm. Empty list = monitor
// all allowed hosts (the default).
func cmdMonitor(args []string) error {
	cfg, err := readConfig(getProjectDir())
	if err != nil {
		return err
	}

	action := "list"
	if len(args) > 0 {
		action = args[0]
	}

	switch action {
	case "list":
		if len(cfg.MonitorHosts) == 0 {
			fmt.Println("monitor-list: empty → monitoring ALL allowed hosts (default)")
			return nil
		}
		fmt.Println("monitor-list (only these hosts are routed through mitm):")
		for _, h := range cfg.MonitorHosts {
			fmt.Printf("  - %s\n", h)
		}
		return nil

	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude monitor add <host>")
		}
		host := strings.ToLower(strings.TrimSpace(args[1]))
		if contains(cfg.MonitorHosts, host) {
			fmt.Printf("already monitored: %s\n", host)
			return nil
		}
		cfg.MonitorHosts = append(cfg.MonitorHosts, host)
		sort.Strings(cfg.MonitorHosts)
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("added to monitor-list: %s\n", host)
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude monitor remove <host>")
		}
		host := strings.ToLower(strings.TrimSpace(args[1]))
		cfg.MonitorHosts = remove(cfg.MonitorHosts, host)
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("removed from monitor-list: %s\n", host)
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	case "clear":
		cfg.MonitorHosts = nil
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Println("monitor-list cleared → monitoring ALL allowed hosts")
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	default:
		return fmt.Errorf("unknown monitor action %q (use list|add|remove|clear)", action)
	}
}

// cmdMitmPorts: sandclaude mitm-ports [list|add <port>|remove <port>|reset]
func cmdMitmPorts(args []string) error {
	cfg, err := readConfig(getProjectDir())
	if err != nil {
		return err
	}

	action := "list"
	if len(args) > 0 {
		action = args[0]
	}

	switch action {
	case "list":
		fmt.Printf("mitm-eligible ports: %s\n", strings.Join(cfg.mitmPortsOrDefault(), ", "))
		fmt.Println("(CONNECT to any other port — ssh, socks, etc. — is direct-dialed)")
		return nil

	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude mitm-ports add <port>")
		}
		port := strings.TrimSpace(args[1])
		if !isPort(port) {
			return fmt.Errorf("invalid port: %q", port)
		}
		ports := cfg.mitmPortsOrDefault()
		if contains(ports, port) {
			fmt.Printf("already present: %s\n", port)
			return nil
		}
		cfg.MitmPorts = append(ports, port)
		sortPorts(cfg.MitmPorts)
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("added mitm port: %s\n", port)
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude mitm-ports remove <port>")
		}
		port := strings.TrimSpace(args[1])
		cfg.MitmPorts = remove(cfg.mitmPortsOrDefault(), port)
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("removed mitm port: %s\n", port)
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	case "reset":
		cfg.MitmPorts = nil
		if err := writeConfig(getProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Println("mitm-ports reset to default (80,443)")
		return applyProxyConfig(applyScope{monitorOrPorts: true})

	default:
		return fmt.Errorf("unknown mitm-ports action %q (use list|add|remove|reset)", action)
	}
}

// cmdSetCred: sandclaude set-cred <host> <header|url_param> <name> <value>
// Adds/updates an injected credential. Writes the project-scoped credentials file
// and (since the addon watches it) the running mitmweb picks it up live.
func cmdSetCred(args []string) error {
	if len(args) < 4 {
		return fmt.Errorf("usage: sandclaude set-cred <host> <header|url_param> <name> <value>")
	}
	host := strings.ToLower(strings.TrimSpace(args[0]))
	kind := args[1]
	name := args[2]
	value := args[3]
	if kind != "header" && kind != "url_param" {
		return fmt.Errorf("second arg must be 'header' or 'url_param', got %q", kind)
	}

	path := projectCredentialsPath()
	creds, err := loadCredsMap(path)
	if err != nil {
		return err
	}
	creds[host] = map[string]string{kind: name, "value": value}
	if err := writeCredsMap(path, creds); err != nil {
		return err
	}
	fmt.Printf("set credential for %s (%s: %s)\n", host, kind, name)
	return applyProxyConfig(applyScope{credentials: true})
}

// cmdUnsetCred: sandclaude unset-cred <host>
func cmdUnsetCred(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: sandclaude unset-cred <host>")
	}
	host := strings.ToLower(strings.TrimSpace(args[0]))
	path := projectCredentialsPath()
	creds, err := loadCredsMap(path)
	if err != nil {
		return err
	}
	if _, ok := creds[host]; !ok {
		fmt.Printf("no credential for %s\n", host)
		return nil
	}
	delete(creds, host)
	if err := writeCredsMap(path, creds); err != nil {
		return err
	}
	fmt.Printf("removed credential for %s\n", host)
	return applyProxyConfig(applyScope{credentials: true})
}

// cmdProxyApply re-materializes the full proxy config to the running proxies —
// the CLI equivalent of the dashboard's Apply button, and a way to push a config
// edited by hand. Reloads both planes (allowlist/monitor + credentials).
func cmdProxyApply() error {
	return applyProxyConfig(applyScope{monitorOrPorts: true, credentials: true})
}

// ----------------------------------------------------------------------------
// Apply — minimal-impact reload (shared by CLI and dashboard).
// ----------------------------------------------------------------------------

// applyScope selects which reload actions run, so a change touches only what it
// affects: monitor-list/ports -> SIGHUP the allowlist-proxy; credentials ->
// nothing here (mitmweb's addon watches the file and reloads within ~1s).
// Neither restarts the container.
type applyScope struct {
	monitorOrPorts bool
	credentials    bool
}

// applyProxyConfig writes the monitor-hosts file (so a restart also sees it) and,
// if a container is running, reloads the allowlist-proxy for monitor/port changes.
// Credential changes need no action here — mitmweb picks them up via mtime watch.
func applyProxyConfig(scope applyScope) error {
	cfg, err := readConfig(getProjectDir())
	if err != nil {
		return err
	}

	if scope.monitorOrPorts {
		monitorPath := monitorHostsPath()
		if len(cfg.MonitorHosts) > 0 {
			if err := writeMonitorHostsFile(monitorPath, cfg.MonitorHosts); err != nil {
				return err
			}
		} else {
			// Empty list -> remove the file so the proxy falls back to monitor-all.
			os.Remove(monitorPath)
		}

		containerName := runningContainerName()
		if dockerContainerRunning(containerName) {
			if err := reloadProxyInContainer(containerName); err != nil {
				return fmt.Errorf("proxy reload failed: %w", err)
			}
			fmt.Printf("✅ reloaded allowlist-proxy in '%s'\n", containerName)
		} else {
			fmt.Println("(no running container — config saved for next start)")
		}
	}

	if scope.credentials {
		// mitmweb's addon watches the credentials file and reloads within ~1s;
		// nothing to signal. Note it so the user knows it took effect.
		fmt.Println("✅ credentials updated (mitmweb reloads automatically within ~1s)")
	}

	return nil
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func monitorHostsPath() string {
	return filepath.Join(getProjectDir(), "monitor-hosts.txt")
}

// writeCredsMap writes a domain->entry credentials map as pretty JSON (0600 —
// it holds secrets).
func writeCredsMap(path string, creds map[string]map[string]string) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func remove(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func isPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n > 0 && n <= 65535
}

// sortPorts sorts a port slice numerically in place.
func sortPorts(ports []string) {
	sort.Slice(ports, func(i, j int) bool {
		return atoiSafe(ports[i]) < atoiSafe(ports[j])
	})
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
