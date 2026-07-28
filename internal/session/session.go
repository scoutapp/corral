// Package session derives the container and tmux-session names sandclaude uses
// to identify a project's running sandbox. Naming is deterministic from the
// workspace path so start, dev, dashboard, and the capture/send/attach commands
// all agree on which container and tmux session belong to a project.
package session

import (
	"fmt"
	"path/filepath"

	"github.com/jackrothrock/sandclaude/internal/config"
)

// ContainerNameForWorkspace returns the container name for a workspace
// (sandclaude_<workspace-basename>).
func ContainerNameForWorkspace(workspace string) string {
	return "sandclaude_" + filepath.Base(workspace)
}

// TmuxSessionNameForContainer derives the host-level tmux session name for a detached
// dev session from its container name. The session name matches the container name
// verbatim (underscores preserved) to stay consistent with the container naming
// convention.
func TmuxSessionNameForContainer(containerName string) string {
	return containerName
}

// TmuxSessionNameForWorkspace derives the host-level tmux session name for a given
// workspace path directly.
func TmuxSessionNameForWorkspace(workspace string) string {
	return TmuxSessionNameForContainer(ContainerNameForWorkspace(workspace))
}

// RunningContainerName returns the running container name for the current project
// (sandclaude_<workspace-basename>), matching the name used by start/dev. Falls back
// to the legacy bare "sandclaude" if no project config is readable.
func RunningContainerName() string {
	if cfg, err := config.ReadConfig(config.GetProjectDir()); err == nil && cfg.Workspace != "" {
		return ContainerNameForWorkspace(cfg.Workspace)
	}
	return "sandclaude"
}

// DetachedSessionName derives the tmux session name for the current project's container.
func DetachedSessionName() (session string, container string, err error) {
	cfg, err := config.ReadConfig(config.GetProjectDir())
	if err != nil {
		return "", "", fmt.Errorf("no project configured — run sandclaude init first")
	}
	container = ContainerNameForWorkspace(cfg.Workspace)
	session = TmuxSessionNameForContainer(container)
	return session, container, nil
}
