package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// SandclaudeHome returns the per-user data directory (~/.sandclaude), overridable
// via $SANDCLAUDE_HOME. It holds the installed asset bundle (assets/) and the
// global proxy-credentials.json.
func SandclaudeHome() string {
	if h := os.Getenv("SANDCLAUDE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home directory: %v", err)
	}
	return filepath.Join(home, ".sandclaude")
}

// AssetsDir returns the Docker build-context asset bundle (Dockerfile,
// entrypoint.sh, launcher.py, proxy-addon.py, allowlist-proxy/, bin/, .claude/).
// Resolution order:
//  1. $SANDCLAUDE_HOME/assets       — only when SANDCLAUDE_HOME is set explicitly
//  2. <bindir>/assets               — installed next to the binary
//  3. <bindir>                      — DEV MODE: running ./sandclaude from the git
//     checkout, where Dockerfile/allowlist-proxy/ sit beside the binary
//  4. ~/.sandclaude/assets          — default installed location
//
// The binary-adjacent cases (2 & 3) intentionally take precedence over the default
// installed location (4) so that running ./sandclaude from a checkout keeps using the
// live checkout assets even after an install created ~/.sandclaude/assets. An explicit
// SANDCLAUDE_HOME (1) always wins for deliberate overrides.
func AssetsDir() string {
	looksLikeAssets := func(dir string) bool {
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
			return false
		}
		if _, err := os.Stat(filepath.Join(dir, "allowlist-proxy")); err != nil {
			return false
		}
		return true
	}

	// 1. Explicit override via SANDCLAUDE_HOME.
	if os.Getenv("SANDCLAUDE_HOME") != "" {
		if cand := filepath.Join(SandclaudeHome(), "assets"); looksLikeAssets(cand) {
			return cand
		}
	}

	// 2 & 3. Relative to the binary (installed-beside, then dev-mode checkout).
	if exePath, err := os.Executable(); err == nil {
		binDir := filepath.Dir(exePath)
		if cand := filepath.Join(binDir, "assets"); looksLikeAssets(cand) {
			return cand
		}
		if looksLikeAssets(binDir) {
			return binDir // dev mode: checkout beside the binary
		}
	}

	// 4. Default installed location. Returned even if it doesn't exist yet, so callers
	// produce a clear "run install.sh" style error rather than an empty path.
	return filepath.Join(SandclaudeHome(), "assets")
}

// SandclaudeDir returns <cwd>/.sandclaude — the per-project directory holding the
// allowlist (allowed-domains.txt[.enc]), logs/, and project/ config.
func SandclaudeDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}
	return filepath.Join(cwd, ".sandclaude")
}

// GetProjectDir returns <cwd>/.sandclaude/project/ — per-project config and state.
func GetProjectDir() string {
	return filepath.Join(SandclaudeDir(), "project")
}

// GetLogsDir returns <cwd>/.sandclaude/logs/ — host-side proxy/mitm logs for this project.
func GetLogsDir() string {
	return filepath.Join(SandclaudeDir(), "logs")
}

// EnsureGitignored adds entry to <cwd>/.gitignore if not already present.
func EnsureGitignored(entry string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	gitignorePath := filepath.Join(cwd, ".gitignore")

	existing, err := os.ReadFile(gitignorePath)
	if os.IsNotExist(err) {
		if writeErr := os.WriteFile(gitignorePath, []byte(entry+"\n"), 0644); writeErr == nil {
			log.Printf("✅ Created .gitignore with %s\n", entry)
		}
		return
	}
	if err != nil {
		return
	}

	// Check if entry already present (match with or without trailing slash).
	trimmed := strings.TrimRight(entry, "/")
	for _, line := range strings.Split(string(existing), "\n") {
		l := strings.TrimSpace(line)
		if l == entry || l == trimmed {
			return
		}
	}

	content := string(existing)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry + "\n"
	if writeErr := os.WriteFile(gitignorePath, []byte(content), 0644); writeErr == nil {
		log.Printf("✅ Added %s to .gitignore\n", entry)
	}
}
