package container

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/creds"
	"github.com/jackrothrock/sandclaude/internal/dashboard"
)

// startProxy starts the mitmweb proxy process
func (sc *SandClaude) startProxy(workspace string) error {
	// Remember the workspace so stopProxy cleans up the same workspace-relative
	// runtime.json that WriteProxyRuntimeState writes below.
	sc.workspace = workspace

	// Re-resolve credentials with lifecycle tracking so any merged temp file gets
	// cleaned up on stopProxy. An explicit SANDCLAUDE_PROXY_CREDS override is honored
	// as-is (no merge, no temp file). Skip when merging isn't applicable.
	if os.Getenv("SANDCLAUDE_PROXY_CREDS") == "" {
		credsFile, tempFile := creds.ResolveCredentialsFileTracked()
		sc.credentialsFile = credsFile
		sc.mergedCredsFile = tempFile
	}

	// Find a free port, starting from the configured base port
	basePort := 9500
	if sc.proxyPort != "" {
		if p, err := strconv.Atoi(sc.proxyPort); err == nil {
			basePort = p
		}
	}
	freePort, err := config.FindFreePort(basePort)
	if err != nil {
		return fmt.Errorf("failed to find free port for proxy: %w", err)
	}
	sc.proxyPort = fmt.Sprintf("%d", freePort)

	webPort, err := config.FindFreePort(8081)
	if err != nil {
		return fmt.Errorf("failed to find free port for mitmweb UI: %w", err)
	}

	log.Printf("Starting proxy on port %s", sc.proxyPort)
	if state, spawned, err := dashboard.EnsureDashboardRunning(); err != nil {
		log.Printf("Warning: could not start dashboard: %v", err)
		log.Printf("Proxy web UI: http://127.0.0.1:%d", webPort)
	} else {
		dashboardURL := fmt.Sprintf("http://127.0.0.1:%d/p/%s?token=%s", state.Port, dashboard.ProjectID(workspace), state.Token)
		log.Printf("Dashboard: %s", dashboardURL)
		// Only pop a browser tab when this start actually launched the dashboard
		// daemon (so N project starts don't open N tabs at the same dashboard), AND
		// only for the foreground path. The detached path (`start` default, `dev`)
		// opens the project's terminal itself after launch — see Run() — so it owns
		// the open and this would just be a duplicate.
		if spawned && !sc.DevMode {
			if err := config.OpenBrowser(dashboardURL); err != nil {
				config.Debugf("failed to open browser: %v", err)
			}
		}
	}
	config.Debugf("Credentials file: %s", sc.credentialsFile)

	// Check if addon script exists
	if _, err := os.Stat(sc.addonScript); os.IsNotExist(err) {
		return fmt.Errorf("addon script not found: %s", sc.addonScript)
	}

	// Open log file for mitmweb output
	logsDir := config.GetLogsDir()
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs dir: %w", err)
	}
	mitmLog := filepath.Join(logsDir, "mitm.log")
	logFile, err := os.OpenFile(mitmLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", mitmLog, err)
	}

	// Ensure the mitmproxy conf dir exists and is writable by the current user.
	// Docker bind-mounts create parent directories as root, so ~/.mitmproxy may be
	// root-owned even though the current user can't write there. Test writability
	// directly (mode bits lie when the owner differs from the current user).
	userHome, _ := os.UserHomeDir()
	confDir := filepath.Join(userHome, ".mitmproxy")
	if err := os.MkdirAll(confDir, 0700); err != nil || !config.IsDirWritable(confDir) {
		confDir = filepath.Join(os.TempDir(), "sandclaude-mitmproxy")
		if err := os.MkdirAll(confDir, 0700); err != nil {
			return fmt.Errorf("failed to create mitmproxy conf dir: %w", err)
		}
		log.Printf("~/.mitmproxy not writable, using confdir: %s", confDir)
	}

	// Start mitmweb
	sc.proxyCmd = exec.Command(
		"mitmweb",
		"--listen-port", sc.proxyPort,
		"--web-port", fmt.Sprintf("%d", webPort),
		"--set", fmt.Sprintf("confdir=%s", confDir),
		"--set", fmt.Sprintf("credentials_file=%s", sc.credentialsFile),
		"--set", "stream_large_bodies=50m", // stream responses >50MB instead of buffering
		"--ssl-insecure",
		"-s", sc.addonScript,
	)

	// Write output to log file
	sc.proxyCmd.Stdout = logFile
	sc.proxyCmd.Stderr = logFile

	if err := sc.proxyCmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start mitmweb: %w", err)
	}

	log.Printf("Proxy started (PID %d), logs: %s", sc.proxyCmd.Process.Pid, mitmLog)

	if err := dashboard.WriteProxyRuntimeState(workspace, webPort, sc.proxyCmd.Process.Pid); err != nil {
		config.Debugf("Warning: failed to write proxy runtime state: %v", err)
	}

	// Give proxy time to start, THEN wait for the CA cert to actually exist on the
	// host. mitmproxy generates ~/.mitmproxy/mitmproxy-ca-cert.pem on first startup;
	// on a slow/cold runner that can take well over the old fixed 2s. startDocker
	// only mounts the CA into the container `if os.Stat(certPath) == nil` — so if we
	// proceed before the file exists, the mount is silently skipped, the container
	// never trusts the proxy, and (with DinD) the cert-injector never starts. This
	// was the ~20% e2e flake ("cert-injector.log should exist"). Block on the file.
	time.Sleep(1 * time.Second)
	if home, herr := os.UserHomeDir(); herr == nil {
		caPath := filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(caPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				log.Printf("Warning: mitmproxy CA cert not present at %s after 30s; "+
					"the container may not trust the proxy (cert-injection may be skipped)", caPath)
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	return nil
}

// stopProxy stops the mitmweb proxy process
func (sc *SandClaude) stopProxy() {
	if sc.proxyCmd != nil && sc.proxyCmd.Process != nil {
		config.Debugf("Stopping proxy (PID %d)...", sc.proxyCmd.Process.Pid)

		// Try graceful shutdown first
		if err := sc.proxyCmd.Process.Signal(syscall.SIGTERM); err != nil {
			log.Printf("Failed to send SIGTERM to proxy: %v", err)
			sc.proxyCmd.Process.Kill()
		}

		sc.proxyCmd.Wait()
		log.Println("Proxy stopped")
	}

	// Remove any merged credentials temp file we created.
	if sc.mergedCredsFile != "" {
		if err := os.Remove(sc.mergedCredsFile); err != nil && !os.IsNotExist(err) {
			config.Debugf("Failed to remove merged credentials temp file %s: %v", sc.mergedCredsFile, err)
		}
		sc.mergedCredsFile = ""
	}

	// Remove the same workspace-relative runtime.json that startProxy wrote.
	// sc.workspace may be empty if stopProxy runs without a prior startProxy
	// (e.g. proxy disabled); RemoveProxyRuntimeState tolerates a missing file.
	if sc.workspace != "" {
		if err := dashboard.RemoveProxyRuntimeState(sc.workspace); err != nil {
			config.Debugf("Failed to remove proxy runtime state: %v", err)
		}
	}
}

// ----------------------------------------------------------------------------
// Project config (project/config.json)
// ----------------------------------------------------------------------------

// Run starts the full sandclaude environment
func (sc *SandClaude) Run(keepDevfiles bool) error {
	log.Println("SandClaude - Secure Claude Code Environment")

	projectDir := config.GetProjectDir()
	cfg, err := config.ReadConfig(projectDir)
	if err != nil {
		return err
	}

	config.Debugf("Config: workspace=%s proxy=%v dind=%v", cfg.Workspace, cfg.ProxyEnabled, cfg.DindEnabled)

	if _, err := os.Stat(cfg.Workspace); os.IsNotExist(err) {
		return fmt.Errorf("workspace not found: %s", cfg.Workspace)
	}

	if err := dashboard.RegisterProject(cfg.Workspace); err != nil {
		config.Debugf("Warning: failed to update project registry: %v", err)
	}

	// If we're already inside a sandclaude container, skip docker entirely.
	// The proxy, firewall, and workspace are set up by the outer entrypoint.
	if os.Getenv("SANDCLAUDE_CONTAINER") == "1" {
		return sc.startDirect(cfg)
	}

	// Check if DinD is enabled (unless --disable-dind was passed)
	if !sc.DisableDind && cfg.DindEnabled {
		sc.dindEnabled = true
		sc.dindPorts = cfg.DindPorts
	}
	sc.seccompMode = cfg.SeccompMode

	// Re-encrypt the allowlist from plaintext so the mounted .enc always reflects
	// the current allowed-domains.txt. Without this, editing the plaintext and then
	// running start/dev silently launches with a stale .enc (the proxy reads the
	// encrypted file, not the plaintext), so newly-added domains stay blocked.
	if cfg.ProxyEnabled {
		if err := creds.SyncEncryptedAllowlist(); err != nil {
			log.Printf("Warning: could not re-encrypt allowlist, using existing .enc: %v", err)
		}
	}

	// Build the image before starting the proxy — the proxy intercepts HTTPS during
	// docker build and presents its own cert, which the build's curl won't trust yet.
	if err := sc.EnsureImage("sandclaude-stable"); err != nil {
		return err
	}

	if cfg.ProxyEnabled {
		sc.proxyEnabled = true
		log.Println("Proxy enabled, starting...")

		if err := sc.startProxy(cfg.Workspace); err != nil {
			return err
		}

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			log.Println("\nReceived interrupt signal, cleaning up...")
			sc.stopProxy()
			os.Exit(0)
		}()
	}

	err = sc.startDocker(cfg, keepDevfiles)

	if sc.proxyEnabled {
		if sc.detachedSession == "" {
			sc.stopProxy()
		} else {
			log.Printf("Note: mitmproxy is still running alongside the detached container. Stop it manually when done.")
		}
	}

	// Browser-first: after a detached start, open the dashboard straight to this
	// project's terminal so the browser is where you interact. (The dashboard
	// daemon is already up — startProxy ensured it.) Skipped for the foreground
	// path, which keeps the classic in-terminal session.
	if err == nil && sc.detachedSession != "" {
		if state, derr := dashboard.ReadDashboardState(); derr == nil && state != nil {
			url := fmt.Sprintf("http://127.0.0.1:%d/p/%s?token=%s", state.Port, dashboard.ProjectID(cfg.Workspace), state.Token)
			log.Printf("Open in dashboard: %s", url)
			if oerr := config.OpenBrowser(url); oerr != nil {
				config.Debugf("failed to open browser: %v", oerr)
			}
		}
	}

	return err
}
