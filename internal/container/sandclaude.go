package container

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/creds"
)

const (
	defaultProxyPort   = "9500"
	mitmwebProcessName = "mitmweb"
)

type SandClaude struct {
	proxyCmd                    *exec.Cmd
	proxyPort                   string
	credentialsFile             string
	addonScript                 string
	proxyEnabled                bool
	DisableFirewall             bool
	PassthroughFirewallAndWrite bool
	DisableDind                 bool
	dindEnabled                 bool
	dindPorts                   []string
	DevMode                     bool   // set by `dev`: launch detached in tmux for closed-loop development
	detachedSession             string // non-empty when container launched in background tmux session
	mergedCredsFile             string // non-empty when a merged temp creds file was written (cleaned up on stop)
}

func NewSandClaude() (*SandClaude, error) {
	// Get configuration from environment
	proxyPort := os.Getenv("SANDCLAUDE_PROXY_PORT")
	if proxyPort == "" {
		proxyPort = defaultProxyPort
	}

	// Resolve the credentials file. An explicit env override wins; otherwise merge
	// the global (~/.sandclaude/proxy-credentials.json) with the per-project override
	// (<cwd>/.sandclaude/project/proxy-credentials.json), project winning per-domain.
	// The real per-domain merge happens in startProxy (which can track the temp file
	// for cleanup); here we just pick a best-effort path.
	credentialsFile := os.Getenv("SANDCLAUDE_PROXY_CREDS")
	if credentialsFile == "" {
		credentialsFile = creds.ResolveCredentialsFile()
	}

	// proxy-addon.py is a HOST-tier asset — loaded by the host's mitmweb, not the
	// sandbox — so it resolves from the host/ bundle, not the sandbox build context.
	addonScript := filepath.Join(config.HostAssetsDir(), "proxy-addon.py")

	return &SandClaude{
		proxyPort:       proxyPort,
		credentialsFile: credentialsFile,
		addonScript:     addonScript,
	}, nil
}
