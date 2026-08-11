package container

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
	sshagent "github.com/scoutapp/corral/internal/ssh"
)

const (
	defaultProxyPort   = "9500"
	mitmwebProcessName = "mitmweb"
)

type Corral struct {
	proxyCmd                    *exec.Cmd
	proxyPort                   string
	credentialsFile             string
	addonScript                 string
	proxyEnabled                bool
	DisableFirewall             bool
	PassthroughFirewallAndWrite bool
	// EnforceAllowlist, when set by the CLI --enforce-allowlist flag, forces the
	// strict allowlist for THIS run regardless of the saved per-project config
	// (which now defaults to passthrough). It's a per-invocation override, not a
	// saved setting.
	EnforceAllowlist bool
	DisableDind      bool
	dindEnabled                 bool
	dindPorts                   []string
	seccompMode                 string          // "" default | "unconfined" | custom profile path
	DevMode                     bool            // set by `dev`: launch detached in tmux for closed-loop development
	detachedSession             string          // non-empty when container launched in background tmux session
	mergedCredsFile             string          // non-empty when a merged temp creds file was written (cleaned up on stop)
	workspace                   string          // project workspace path; set by startProxy so stopProxy can clean up the workspace-relative runtime.json
	sshAgent                    *sshagent.Agent // non-nil when a scoped ssh-agent was started for this run (mounted + torn down on stop)
}

func NewSandClaude() (*Corral, error) {
	// Get configuration from environment
	proxyPort := os.Getenv("CORRAL_PROXY_PORT")
	if proxyPort == "" {
		proxyPort = defaultProxyPort
	}

	// Resolve the credentials file. An explicit env override wins; otherwise merge
	// the global (~/.corral/proxy-credentials.json) with the per-project override
	// (<cwd>/.corral/project/proxy-credentials.json), project winning per-domain.
	// The real per-domain merge happens in startProxy (which can track the temp file
	// for cleanup); here we just pick a best-effort path.
	credentialsFile := os.Getenv("CORRAL_PROXY_CREDS")
	if credentialsFile == "" {
		credentialsFile = creds.ResolveCredentialsFile()
	}

	// proxy-addon.py is a HOST-tier asset — loaded by the host's mitmweb, not the
	// sandbox — so it resolves from the host/ bundle, not the sandbox build context.
	addonScript := filepath.Join(config.HostAssetsDir(), "proxy-addon.py")

	return &Corral{
		proxyPort:       proxyPort,
		credentialsFile: credentialsFile,
		addonScript:     addonScript,
	}, nil
}
