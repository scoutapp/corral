package proxy

import (
	"fmt"
	"os"
	"os/exec"
)

// ReloadProxyInContainer makes the running allowlist-proxy pick up the freshly
// re-encrypted allowlist. The proxy reads its allowlist from /tmp/allowed-domains.txt.enc
// (a startup copy proxyuser can read), while the host's edits land on the bind-mounted
// /home/claude/allowed-domains.txt.enc — so we must re-copy that into /tmp before the
// SIGHUP, otherwise the proxy reloads stale content. Both steps run as root: the proxy
// runs as proxyuser, which the default exec user (claude) cannot signal.
func ReloadProxyInContainer(containerName string) error {
	// The monitor-hosts file is bind-mounted only when it exists at `docker run`
	// time, so a project that enables the monitor-list for the first time on a
	// running container has no mount to reload from. To make first-enable work
	// live (no restart), docker cp the current host file into the container before
	// SIGHUP. If the host file doesn't exist (monitor-all default), remove any
	// stale copy so the proxy falls back to monitoring all hosts.
	hostMonitor := MonitorHostsPath()
	if _, err := os.Stat(hostMonitor); err == nil {
		cp := exec.Command("docker", "cp", hostMonitor, containerName+":/tmp/monitor-hosts.txt")
		cp.Stderr = os.Stderr
		if err := cp.Run(); err != nil {
			return fmt.Errorf("copy monitor-hosts into container: %w", err)
		}
	} else {
		// Best-effort removal of a stale in-container copy.
		exec.Command("docker", "exec", "-u", "root", containerName, "rm", "-f", "/tmp/monitor-hosts.txt").Run()
	}

	// Same live-copy treatment for credential-hosts (always-mitm hosts), so a
	// credential added/removed on a running container updates forced interception
	// without a restart.
	hostCred := CredentialHostsPath()
	if _, err := os.Stat(hostCred); err == nil {
		cp := exec.Command("docker", "cp", hostCred, containerName+":/tmp/credential-hosts.txt")
		cp.Stderr = os.Stderr
		if err := cp.Run(); err != nil {
			return fmt.Errorf("copy credential-hosts into container: %w", err)
		}
	} else {
		exec.Command("docker", "exec", "-u", "root", containerName, "rm", "-f", "/tmp/credential-hosts.txt").Run()
	}

	// Re-copy the allowlist into the proxyuser-readable /tmp path, ensure the
	// monitor + credential-hosts copies are readable, then SIGHUP so the proxy
	// reloads all three.
	cmd := exec.Command("docker", "exec", "-u", "root", containerName, "bash", "-c",
		"cp /home/claude/allowed-domains.txt.enc /tmp/allowed-domains.txt.enc && "+
			"chmod 644 /tmp/allowed-domains.txt.enc && "+
			"chmod 644 /tmp/monitor-hosts.txt 2>/dev/null || true && "+
			"chmod 644 /tmp/credential-hosts.txt 2>/dev/null || true && "+
			"pkill -HUP -x allowlist-proxy")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
