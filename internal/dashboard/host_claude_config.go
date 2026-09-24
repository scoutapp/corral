package dashboard

import (
	"os"
	"path/filepath"

	"github.com/scoutapp/corral/internal/config"
)

// The host interactive `claude` (the host-Claude terminal) must write its session
// transcripts somewhere the conversation tailer can attribute UNAMBIGUOUSLY to the
// host origin. By default Claude Code writes them to ~/.claude/projects/<slug>/,
// the SAME dir the sandbox's Claude writes to via the bind mount — so on disk the
// two are indistinguishable.
//
// Fix: point the host terminal at a DEDICATED CLAUDE_CONFIG_DIR (hostClaudeConfigDir)
// so its transcripts land in <dir>/projects/, a tree corral owns and the tailer
// reads as host-origin. CLAUDE_CONFIG_DIR relocates the WHOLE config dir, not just
// projects/, so we seed the dedicated dir by symlinking every entry of ~/.claude
// into it EXCEPT projects/ — auth (Linux .credentials.json), settings, plugins,
// skills, agents, etc. all remain shared, so /login, settings, and skills behave
// exactly as in the user's normal claude; only the transcript stream diverges.
// (On macOS auth is in the Keychain and shared regardless.)

// hostClaudeConfigDir is the dedicated CLAUDE_CONFIG_DIR for host-terminal claude.
func hostClaudeConfigDir() string {
	return filepath.Join(config.CorralHome(), "host-claude-config")
}

// hostClaudeProjectsDir is where host-terminal claude writes its transcripts —
// the tailer reads this tree as host-origin.
func hostClaudeProjectsDir() string {
	return filepath.Join(hostClaudeConfigDir(), "projects")
}

// ensureHostClaudeConfigDir creates the dedicated config dir and (re)links every
// ~/.claude entry except projects/ into it, so the host terminal shares the user's
// auth/settings/plugins but keeps a separate transcript tree. Idempotent and
// best-effort: it's called on each host-terminal open, refreshing links so a newly
// added ~/.claude entry (e.g. a freshly installed plugin) shows up without a
// restart. Returns the config dir, or "" if it couldn't be prepared.
func ensureHostClaudeConfigDir() string {
	cfgDir := hostClaudeConfigDir()
	if cfgDir == "" {
		return ""
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return ""
	}
	// Our own real projects/ dir — NOT a link to ~/.claude/projects, that's the
	// whole point of the separation.
	if err := os.MkdirAll(filepath.Join(cfgDir, "projects"), 0o755); err != nil {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return cfgDir // no source to link; a bare config dir still works (fresh login)
	}
	src := filepath.Join(home, ".claude")
	entries, err := os.ReadDir(src)
	if err != nil {
		return cfgDir // ~/.claude absent (fresh machine); nothing to share yet
	}
	for _, e := range entries {
		name := e.Name()
		if name == "projects" {
			continue // keep transcripts separate
		}
		linkPath := filepath.Join(cfgDir, name)
		target := filepath.Join(src, name)
		// Refresh: if a link already points at target, leave it; otherwise (re)create.
		// Only ever replace a symlink we own — never clobber a real file/dir a user
		// might have placed here.
		if existing, lerr := os.Readlink(linkPath); lerr == nil {
			if existing == target {
				continue
			}
			_ = os.Remove(linkPath)
		} else if _, serr := os.Lstat(linkPath); serr == nil {
			// A non-symlink already sits here (user-placed or a stale real dir); don't
			// touch it.
			continue
		}
		_ = os.Symlink(target, linkPath)
	}
	return cfgDir
}
