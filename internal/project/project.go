// Package project holds corral's project-lifecycle logic that is shared
// between the CLI (interactive `corral init`) and the dashboard
// (create-project flow). It lives in its own package because it needs both
// config and creds, and creds already imports config — so this can't live in
// config without an import cycle.
package project

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
)

// InitOptions carries the non-interactive settings for a new project. The CLI
// gathers these from prompts; the dashboard sets them from a request.
type InitOptions struct {
	ProxyEnabled        bool
	LaunchTmux          bool
	DindEnabled         bool
	DindPorts           []string
	PassthroughFirewall bool // "permissive but observed" mode (proxy on, allow+log, direct TCP ok)
	Source              *config.ProjectSource // PR/issue this project was spawned from
}

// InitProject creates a project's on-disk state under <workspace>/.corral:
// writes config.json, generates the allowlist encryption key, and seeds +
// encrypts the allowlist. It is the non-interactive core factored out of the
// CLI's cmdInit so the dashboard can create projects without stdin prompts.
//
// The workspace directory is created if missing. Returns an error if the project
// is already initialized (so callers can surface "already exists").
func InitProject(workspace string, opts InitOptions) (*config.ProjectConfig, error) {
	if workspace == "" {
		return nil, fmt.Errorf("workspace path is required")
	}
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return nil, fmt.Errorf("create workspace %s: %w", workspace, err)
	}

	projectDir := config.ProjectDirFor(workspace)
	if _, err := os.Stat(projectDir); err == nil {
		return nil, fmt.Errorf("project already initialized at %s", projectDir)
	}
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}

	cfg := &config.ProjectConfig{
		Workspace:           workspace,
		ProxyEnabled:        opts.ProxyEnabled,
		LaunchTmux:          opts.LaunchTmux,
		DindEnabled:         opts.DindEnabled,
		DindPorts:           opts.DindPorts,
		PassthroughFirewall: opts.PassthroughFirewall,
		Source:              opts.Source,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	if err := config.WriteConfig(projectDir, cfg); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	keyHex, err := writeAllowlistKey(projectDir)
	if err != nil {
		return nil, err
	}
	if err := seedAndEncryptAllowlist(workspace, keyHex); err != nil {
		return nil, err
	}
	return cfg, nil
}

// writeAllowlistKey generates and stores the 32-byte allowlist encryption key
// (as hex) at <projectDir>/.allowlist-key, returning the hex string.
func writeAllowlistKey(projectDir string) (string, error) {
	keyData := make([]byte, 32)
	if _, err := rand.Read(keyData); err != nil {
		return "", fmt.Errorf("generate encryption key: %w", err)
	}
	keyHex := fmt.Sprintf("%x", keyData)
	keyPath := filepath.Join(projectDir, ".allowlist-key")
	if err := os.WriteFile(keyPath, []byte(keyHex), 0600); err != nil {
		return "", fmt.Errorf("write encryption key: %w", err)
	}
	return keyHex, nil
}

// seedAndEncryptAllowlist copies the shipped default allowlist into the project
// (if absent) and writes its encrypted form, mirroring cmdInit.
func seedAndEncryptAllowlist(workspace, keyHex string) error {
	scDir := config.CorralDirFor(workspace)
	if err := os.MkdirAll(scDir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", scDir, err)
	}
	seedPath := filepath.Join(config.AssetsDir(), "allowlist-proxy", "allowed-domains.txt")
	plaintextPath := filepath.Join(scDir, "allowed-domains.txt")
	encPath := filepath.Join(scDir, "allowed-domains.txt.enc")

	if _, err := os.Stat(plaintextPath); os.IsNotExist(err) {
		seed, rerr := os.ReadFile(seedPath)
		if rerr != nil {
			return fmt.Errorf("read allowlist seed %s: %w (is corral installed? run install.sh)", seedPath, rerr)
		}
		if werr := os.WriteFile(plaintextPath, seed, 0644); werr != nil {
			return fmt.Errorf("write %s: %w", plaintextPath, werr)
		}
	}

	plaintext, err := os.ReadFile(plaintextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plaintextPath, err)
	}
	key, err := creds.AllowlistDeriveKey(keyHex)
	if err != nil {
		return err
	}
	ciphertext, err := creds.AllowlistEncrypt(key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt allowlist: %w", err)
	}
	if err := os.WriteFile(encPath, ciphertext, 0644); err != nil {
		return fmt.Errorf("write %s: %w", encPath, err)
	}
	return nil
}
