// Package session derives the container and tmux-session names sandclaude uses
// to identify a project's running sandbox. Naming is deterministic from the
// workspace path so start, dev, dashboard, and the capture/send/attach commands
// all agree on which container and tmux session belong to a project.
package session

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/config"
)

// ContainerNameForWorkspace returns the container name for a workspace:
// sandclaude_<basename>_<sha8>. The basename keeps the name human-readable; the
// hash suffix makes it unique per workspace PATH, so two different workspaces
// that share a basename (e.g. ephemeral clones of the same repo living in
// separate directories) get distinct containers instead of colliding — which is
// required for running multiple sandclaudes on the same repo at once. Mirrors
// config.DindVolumeName's hash approach so container and DinD volume stay unique
// together. The name is fully derived from the path (no stored state), so every
// caller agrees on it.
func ContainerNameForWorkspace(workspace string) string {
	h := sha256.Sum256([]byte(workspace))
	base := sanitizeDockerName(filepath.Base(workspace))
	return fmt.Sprintf("sandclaude_%s_%x", base, h[:4])
}

// sanitizeDockerName maps characters Docker disallows in container names to '_'.
// Docker allows [a-zA-Z0-9][a-zA-Z0-9_.-]*; a workspace basename can contain
// spaces or other characters, so normalize them.
func sanitizeDockerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "ws"
	}
	return out
}

// TmuxSessionNameForContainer derives the host-level tmux session name for a
// detached dev session from its container name.
//
// tmux parses '.' and ':' in a target as window/pane separators (session.window
// or session:window), so a container name like "sandclaude_my.app" (a project in
// a directory named "my.app") would make tmux look for window "app" in session
// "sandclaude_my" — new-session/has-session/kill-session/capture-pane all
// mis-resolve, breaking start/capture/send/attach. Docker permits '.'/':' in
// container names, so we sanitize only for the tmux layer. Every tmux operation
// derives its target from this one function, so they stay mutually consistent.
func TmuxSessionNameForContainer(containerName string) string {
	return sanitizeTmuxName(containerName)
}

// sanitizeTmuxName replaces characters tmux treats specially in a target name
// ('.' and ':') with '_'. The mapping is deterministic and collision-tolerant
// enough for our names (all begin with the fixed "sandclaude_" prefix).
func sanitizeTmuxName(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
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
