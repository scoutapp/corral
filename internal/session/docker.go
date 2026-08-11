package session

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// RequireTmux returns a clear, actionable error when `tmux` isn't on PATH. tmux
// is a hard host dependency: corral runs the interactive container inside a
// host tmux session (so it survives detach/reattach and the dashboard can attach
// to it). Called as a preflight by start/dev/dashboard so a missing tmux fails
// with an install hint instead of a cryptic "session not running" downstream.
func RequireTmux() error {
	if _, err := exec.LookPath("tmux"); err == nil {
		return nil
	}
	hint := "install it and try again"
	switch runtime.GOOS {
	case "darwin":
		hint = "install it with: brew install tmux"
	case "linux":
		hint = "install it with your package manager, e.g.: apt-get install tmux"
	}
	return fmt.Errorf("corral needs tmux on the host but it isn't installed — %s", hint)
}

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
