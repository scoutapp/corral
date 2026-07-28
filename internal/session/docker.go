package session

import (
	"os/exec"
	"strings"
	"syscall"
)

// DockerContainerRunning reports whether a container with the given name is
// currently running. Mirrors the same `docker inspect --format={{.State.Running}}`
// check already used at cmdFirewallReload (main.go) and cmdFirewallMonitor's
// existence check, factored here so the dashboard doesn't duplicate it a third time.
func DockerContainerRunning(containerName string) bool {
	out, err := exec.Command("docker", "inspect", "--format={{.State.Running}}", containerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// TmuxSessionExists mirrors the `tmux has-session` check already used in
// startDetached/startDirect.
func TmuxSessionExists(sessionName string) bool {
	return exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil
}

// PidAlive reports whether a process with the given pid currently exists.
// Signal 0 performs no actual signal delivery, just existence/permission checks.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}
