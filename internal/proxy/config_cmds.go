package proxy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
	"github.com/scoutapp/corral/internal/session"
)

// ----------------------------------------------------------------------------
// Selective-mitm CLI: monitor-list, mitm-ports, credentials, and the unified
// proxy-apply that re-materializes config to the running proxies without a
// restart. These mirror the dashboard control plane (Layers 4-5) so every
// dashboard action has a scriptable CLI equivalent.
//
// Reload granularity is deliberately minimal (see ApplyProxyConfig): a monitor/
// port change SIGHUPs only the allowlist-proxy; a credential change touches only
// mitmweb (its addon watches the file). Neither restarts the container.
// ----------------------------------------------------------------------------

// CmdMonitor: sandclaude monitor [list|add <host>|remove <host>|clear]
// Manages the monitor-list — the hosts routed through mitm. Empty list = monitor
// all allowed hosts (the default).
func CmdMonitor(args []string) error {
	cfg, err := config.ReadConfig(config.GetProjectDir())
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
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("added to monitor-list: %s\n", host)
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude monitor remove <host>")
		}
		host := strings.ToLower(strings.TrimSpace(args[1]))
		cfg.MonitorHosts = remove(cfg.MonitorHosts, host)
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("removed from monitor-list: %s\n", host)
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	case "clear":
		cfg.MonitorHosts = nil
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Println("monitor-list cleared → monitoring ALL allowed hosts")
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	default:
		return fmt.Errorf("unknown monitor action %q (use list|add|remove|clear)", action)
	}
}

// CmdMitmPorts: sandclaude mitm-ports [list|add <port>|remove <port>|reset]
func CmdMitmPorts(args []string) error {
	cfg, err := config.ReadConfig(config.GetProjectDir())
	if err != nil {
		return err
	}

	action := "list"
	if len(args) > 0 {
		action = args[0]
	}

	switch action {
	case "list":
		fmt.Printf("mitm-eligible ports: %s\n", strings.Join(cfg.MitmPortsOrDefault(), ", "))
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
		ports := cfg.MitmPortsOrDefault()
		if contains(ports, port) {
			fmt.Printf("already present: %s\n", port)
			return nil
		}
		cfg.MitmPorts = append(ports, port)
		sortPorts(cfg.MitmPorts)
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("added mitm port: %s\n", port)
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: sandclaude mitm-ports remove <port>")
		}
		port := strings.TrimSpace(args[1])
		cfg.MitmPorts = remove(cfg.MitmPortsOrDefault(), port)
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Printf("removed mitm port: %s\n", port)
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	case "reset":
		cfg.MitmPorts = nil
		if err := config.WriteConfig(config.GetProjectDir(), cfg); err != nil {
			return err
		}
		fmt.Println("mitm-ports reset to default (80,443)")
		return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true})

	default:
		return fmt.Errorf("unknown mitm-ports action %q (use list|add|remove|reset)", action)
	}
}

// CmdSetCred: sandclaude set-cred <host> <header|url_param> <name> <value>
// Adds/updates an injected credential. Writes the project-scoped credentials file
// and (since the addon watches it) the running mitmweb picks it up live.
func CmdSetCred(args []string) error {
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

	path := creds.ProjectCredentialsPath()
	credsMap, err := creds.LoadCredsMap(path)
	if err != nil {
		return err
	}
	credsMap[host] = map[string]string{kind: name, "value": value}
	if err := creds.WriteCredsMap(path, credsMap); err != nil {
		return err
	}
	fmt.Printf("set credential for %s (%s: %s)\n", host, kind, name)
	return ApplyProxyConfig(ApplyScope{Credentials: true})
}

// CmdUnsetCred: sandclaude unset-cred <host>
func CmdUnsetCred(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: sandclaude unset-cred <host>")
	}
	host := strings.ToLower(strings.TrimSpace(args[0]))
	path := creds.ProjectCredentialsPath()
	credsMap, err := creds.LoadCredsMap(path)
	if err != nil {
		return err
	}
	if _, ok := credsMap[host]; !ok {
		fmt.Printf("no credential for %s\n", host)
		return nil
	}
	delete(credsMap, host)
	if err := creds.WriteCredsMap(path, credsMap); err != nil {
		return err
	}
	fmt.Printf("removed credential for %s\n", host)
	return ApplyProxyConfig(ApplyScope{Credentials: true})
}

// CmdProxyApply re-materializes the full proxy config to the running proxies —
// the CLI equivalent of the dashboard's Apply button, and a way to push a config
// edited by hand. Reloads both planes (allowlist/monitor + credentials).
func CmdProxyApply() error {
	return ApplyProxyConfig(ApplyScope{MonitorOrPorts: true, Credentials: true})
}

// ----------------------------------------------------------------------------
// Apply — minimal-impact reload (shared by CLI and dashboard).
// ----------------------------------------------------------------------------

// ApplyScope selects which reload actions run, so a change touches only what it
// affects: monitor-list/ports -> SIGHUP the allowlist-proxy; credentials ->
// nothing here (mitmweb's addon watches the file and reloads within ~1s).
// Neither restarts the container.
type ApplyScope struct {
	MonitorOrPorts bool
	Credentials    bool
}

// ApplyProxyConfig writes the monitor-hosts file (so a restart also sees it) and,
// if a container is running, reloads the allowlist-proxy for monitor/port changes.
// Credential changes need no action here — mitmweb picks them up via mtime watch.
func ApplyProxyConfig(scope ApplyScope) error {
	cfg, err := config.ReadConfig(config.GetProjectDir())
	if err != nil {
		return err
	}

	if scope.MonitorOrPorts {
		monitorPath := MonitorHostsPath()
		// Always write the file (even empty) rather than removing it: the file is a
		// bind-mount target, and a missing source makes Docker recreate it as a
		// directory on the next start (breaking all future writes). The proxy treats
		// an empty file as "monitor all", same as absent.
		if err := config.WriteMonitorHostsFile(monitorPath, cfg.ResolveMonitorHosts()); err != nil {
			return err
		}

		containerName := session.RunningContainerName()
		if session.DockerContainerRunning(containerName) {
			if err := ReloadProxyInContainer(containerName); err != nil {
				return fmt.Errorf("proxy reload failed: %w", err)
			}
			fmt.Printf("✅ reloaded allowlist-proxy in '%s'\n", containerName)
		} else {
			fmt.Println("(no running container — config saved for next start)")
		}
	}

	if scope.Credentials {
		// mitmweb's addon watches the credentials file and reloads the VALUES within
		// ~1s. But the allowlist-proxy must also learn the current set of
		// credentialed HOSTS (it force-mitms them) — so rewrite credential-hosts.txt
		// and SIGHUP the proxy so a newly-credentialed host starts being intercepted
		// (or a de-credentialed one stops being forced) without a restart.
		if err := creds.WriteCredentialHostsFile(CredentialHostsPath(), creds.CredentialHostnames()); err != nil {
			return fmt.Errorf("write credential-hosts: %w", err)
		}
		containerName := session.RunningContainerName()
		if session.DockerContainerRunning(containerName) {
			if err := ReloadProxyInContainer(containerName); err != nil {
				return fmt.Errorf("proxy reload failed: %w", err)
			}
		}
		fmt.Println("✅ credentials updated (mitmweb reloads values ~1s; proxy interception refreshed)")
	}

	return nil
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// MonitorHostsPath is the host-side path of the project's monitor-hosts file.
func MonitorHostsPath() string {
	return filepath.Join(config.GetProjectDir(), "monitor-hosts.txt")
}

// CredentialHostsPath is the host-side path of the project's credential-hosts
// file (hostnames with an injected credential — always mitm'd).
func CredentialHostsPath() string {
	return filepath.Join(config.GetProjectDir(), "credential-hosts.txt")
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
