package app

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProxyPort   = "9500"
	mitmwebProcessName = "mitmweb"
)

// debugMode enables verbose debug logging when set to true via --debug flag.
var debugMode bool

// debugf logs a formatted message only when debug mode is enabled.
func debugf(format string, args ...any) {
	if debugMode {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// debugln logs a message only when debug mode is enabled.
func debugln(args ...any) {
	if debugMode {
		log.Println(append([]any{"[DEBUG]"}, args...)...)
	}
}

// dindVolumeName returns a deterministic Docker named volume for a workspace's
// inner Docker data root. Named volumes sidestep the "lchown /proc: permission
// denied" error that bind mounts hit when Docker extracts layers containing /proc.
func dindVolumeName(workspace string) string {
	h := sha256.Sum256([]byte(workspace))
	return fmt.Sprintf("sandclaude-dind-%x", h[:6])
}

type SandClaude struct {
	proxyCmd                    *exec.Cmd
	proxyPort                   string
	credentialsFile             string
	addonScript                 string
	proxyEnabled                bool
	disableFirewall             bool
	passthroughFirewallAndWrite bool
	disableDind                 bool
	dindEnabled                 bool
	dindPorts                   []string
	devMode                     bool   // set by `dev`: launch detached in tmux for closed-loop development
	detachedSession             string // non-empty when container launched in background tmux session
	mergedCredsFile             string // non-empty when a merged temp creds file was written (cleaned up on stop)
}

// shellQuote returns a single-quoted, shell-safe version of s (equivalent to Python's shlex.quote).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildShellCommand returns a single shell command string with all parts properly quoted.
func buildShellCommand(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
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
		credentialsFile = resolveCredentialsFile()
	}

	addonScript := filepath.Join(assetsDir(), "proxy-addon.py")

	return &SandClaude{
		proxyPort:       proxyPort,
		credentialsFile: credentialsFile,
		addonScript:     addonScript,
	}, nil
}

// askYesNo prompts the user with a yes/no question
func askYesNo(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// findFreePort returns the first available TCP port starting from startPort.
func findFreePort(startPort int) (int, error) {
	debugf("Scanning for free port starting at %d", startPort)
	for port := startPort; port < startPort+100; port++ {
		// Check both 0.0.0.0 (what mitmproxy uses for --listen-port) and
		// 127.0.0.1 (what mitmweb uses for --web-port). A port is only free
		// if both succeed.
		addr1 := fmt.Sprintf("0.0.0.0:%d", port)
		ln1, err1 := net.Listen("tcp", addr1)
		if err1 != nil {
			debugf("Port %d unavailable on 0.0.0.0: %v", port, err1)
			continue
		}
		ln1.Close()

		addr2 := fmt.Sprintf("127.0.0.1:%d", port)
		ln2, err2 := net.Listen("tcp", addr2)
		if err2 != nil {
			debugf("Port %d unavailable on 127.0.0.1: %v", port, err2)
			continue
		}
		ln2.Close()

		debugf("Found free port: %d", port)
		return port, nil
	}
	return 0, fmt.Errorf("no free port found in range %d-%d", startPort, startPort+99)
}

// isDirWritable reports whether the current process can create files in dir.
func isDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// openBrowser best-effort opens url in the user's default browser. Launching
// is a convenience on top of the printed URL, never a requirement, so callers
// should treat a returned error as non-fatal (e.g. log at debug level).
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
	}
	return cmd.Start()
}

// startProxy starts the mitmweb proxy process
func (sc *SandClaude) startProxy(workspace string) error {
	// Re-resolve credentials with lifecycle tracking so any merged temp file gets
	// cleaned up on stopProxy. An explicit SANDCLAUDE_PROXY_CREDS override is honored
	// as-is (no merge, no temp file). Skip when merging isn't applicable.
	if os.Getenv("SANDCLAUDE_PROXY_CREDS") == "" {
		credsFile, tempFile := resolveCredentialsFileTracked()
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
	freePort, err := findFreePort(basePort)
	if err != nil {
		return fmt.Errorf("failed to find free port for proxy: %w", err)
	}
	sc.proxyPort = fmt.Sprintf("%d", freePort)

	webPort, err := findFreePort(8081)
	if err != nil {
		return fmt.Errorf("failed to find free port for mitmweb UI: %w", err)
	}

	log.Printf("Starting proxy on port %s", sc.proxyPort)
	if state, spawned, err := ensureDashboardRunning(); err != nil {
		log.Printf("Warning: could not start dashboard: %v", err)
		log.Printf("Proxy web UI: http://127.0.0.1:%d", webPort)
	} else {
		dashboardURL := fmt.Sprintf("http://127.0.0.1:%d/p/%s?token=%s", state.Port, projectID(workspace), state.Token)
		log.Printf("Dashboard: %s", dashboardURL)
		// Only pop a browser tab when this start actually launched the dashboard
		// daemon (so N project starts don't open N tabs at the same dashboard), AND
		// only for the foreground path. The detached path (`start` default, `dev`)
		// opens the project's terminal itself after launch — see Run() — so it owns
		// the open and this would just be a duplicate.
		if spawned && !sc.devMode {
			if err := openBrowser(dashboardURL); err != nil {
				debugf("failed to open browser: %v", err)
			}
		}
	}
	debugf("Credentials file: %s", sc.credentialsFile)

	// Check if addon script exists
	if _, err := os.Stat(sc.addonScript); os.IsNotExist(err) {
		return fmt.Errorf("addon script not found: %s", sc.addonScript)
	}

	// Open log file for mitmweb output
	logsDir := getLogsDir()
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
	if err := os.MkdirAll(confDir, 0700); err != nil || !isDirWritable(confDir) {
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

	if err := writeProxyRuntimeState(webPort, sc.proxyCmd.Process.Pid); err != nil {
		debugf("Warning: failed to write proxy runtime state: %v", err)
	}

	// Give proxy time to start
	time.Sleep(2 * time.Second)

	return nil
}

// stopProxy stops the mitmweb proxy process
func (sc *SandClaude) stopProxy() {
	if sc.proxyCmd != nil && sc.proxyCmd.Process != nil {
		debugf("Stopping proxy (PID %d)...", sc.proxyCmd.Process.Pid)

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
			debugf("Failed to remove merged credentials temp file %s: %v", sc.mergedCredsFile, err)
		}
		sc.mergedCredsFile = ""
	}

	if err := os.Remove(proxyRuntimeStatePath()); err != nil && !os.IsNotExist(err) {
		debugf("Failed to remove proxy runtime state: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Project config (project/config.json)
// ----------------------------------------------------------------------------

type ProjectConfig struct {
	Workspace    string   `json:"workspace"`
	ProxyEnabled bool     `json:"proxy_enabled,omitempty"`
	DindEnabled  bool     `json:"dind_enabled,omitempty"`
	DindPorts    []string `json:"dind_ports,omitempty"`
	LaunchTmux   bool     `json:"launch_tmux,omitempty"`

	// Selective mitm (see allowlist-proxy). MonitorHosts, when non-empty, is the
	// set of hosts routed through mitmweb for full interception + credential
	// injection; every other allowed host is direct-dialed (still logged, not
	// decrypted). Empty = monitor all allowed hosts (default).
	MonitorHosts []string `json:"monitor_hosts,omitempty"`
	// MitmPorts are the destination ports eligible for mitm; CONNECT to any other
	// port (ssh, SOCKS, …) is direct-dialed. Empty = default 80,443.
	MitmPorts []string `json:"mitm_ports,omitempty"`

	CreatedAt string `json:"created_at"`
}

// mitmPortsOrDefault returns the configured mitm ports, or the 80,443 default
// when unset — centralizing the default so start, apply, and the dashboard agree.
func (c *ProjectConfig) mitmPortsOrDefault() []string {
	if len(c.MitmPorts) == 0 {
		return []string{"80", "443"}
	}
	return c.MitmPorts
}

func readConfig(projectDir string) (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("config not found — run: sandclaude init")
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w", err)
	}
	return &cfg, nil
}

func writeConfig(projectDir string, cfg *ProjectConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectDir, "config.json"), data, 0600)
}

// writeMonitorHostsFile materializes the monitor-list to a newline-separated
// plaintext file (0644 so proxyuser in the container can read the bind-mount).
// The file is the on-disk form the allowlist-proxy's --monitorlist reads.
func writeMonitorHostsFile(path string, hosts []string) error {
	content := strings.Join(hosts, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// sandclaudeHome returns the per-user data directory (~/.sandclaude), overridable
// via $SANDCLAUDE_HOME. It holds the installed asset bundle (assets/) and the
// global proxy-credentials.json.
func sandclaudeHome() string {
	if h := os.Getenv("SANDCLAUDE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home directory: %v", err)
	}
	return filepath.Join(home, ".sandclaude")
}

// assetsDir returns the Docker build-context asset bundle (Dockerfile,
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
func assetsDir() string {
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
		if cand := filepath.Join(sandclaudeHome(), "assets"); looksLikeAssets(cand) {
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
	return filepath.Join(sandclaudeHome(), "assets")
}

// sandclaudeDir returns <cwd>/.sandclaude — the per-project directory holding the
// allowlist (allowed-domains.txt[.enc]), logs/, and project/ config.
func sandclaudeDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}
	return filepath.Join(cwd, ".sandclaude")
}

// getProjectDir returns <cwd>/.sandclaude/project/ — per-project config and state.
func getProjectDir() string {
	return filepath.Join(sandclaudeDir(), "project")
}

// getLogsDir returns <cwd>/.sandclaude/logs/ — host-side proxy/mitm logs for this project.
func getLogsDir() string {
	return filepath.Join(sandclaudeDir(), "logs")
}

// startDocker starts the Docker container with Claude Code
func (sc *SandClaude) startDocker(cfg *ProjectConfig, keepDevfiles bool) error {
	workspace := cfg.Workspace
	// Build image if needed
	imageName := "sandclaude-stable"
	if err := sc.ensureImage(imageName); err != nil {
		return err
	}

	log.Printf("Starting sandclaude (workspace: %s)", workspace)

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Build docker args
	containerName := containerNameForWorkspace(workspace)
	args := []string{"run", "--rm", "-it", "--name", containerName}

	// DinD requires --privileged (superset of NET_ADMIN + NET_RAW + SYS_ADMIN).
	// Without DinD, use minimal capabilities.
	if sc.dindEnabled {
		args = append(args, "--privileged")
	} else {
		args = append(args, "--cap-add=NET_ADMIN", "--cap-add=NET_RAW")
	}

	if sc.disableFirewall {
		args = append(args, "-e", "DISABLE_FIREWALL=1")
	} else if sc.passthroughFirewallAndWrite {
		args = append(args, "-e", "DISABLE_FIREWALL_AND_WRITE=1")
		// Still need the allowlist key and file for the proxy to start
		projectDir := getProjectDir()
		keyPath := filepath.Join(projectDir, ".allowlist-key")
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("encryption key not found at %s\nRun 'sandclaude init' to generate it", keyPath)
		}
		args = append(args, "-e", fmt.Sprintf("ALLOWLIST_KEY=%s", strings.TrimSpace(string(keyData))))

		encPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt.enc")
		if _, err := os.Stat(encPath); os.IsNotExist(err) {
			return fmt.Errorf("encrypted allowlist not found at %s\nRun 'sandclaude firewall-reload' to create it", encPath)
		}
		os.Chmod(encPath, 0644)
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/allowed-domains.txt.enc:ro", encPath))
	} else {
		// Read encryption key from project
		projectDir := getProjectDir()
		keyPath := filepath.Join(projectDir, ".allowlist-key")
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("encryption key not found at %s\nRun 'sandclaude init' to generate it", keyPath)
		}
		args = append(args, "-e", fmt.Sprintf("ALLOWLIST_KEY=%s", strings.TrimSpace(string(keyData))))

		// Mount the encrypted allowlist file
		encPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt.enc")
		if _, err := os.Stat(encPath); os.IsNotExist(err) {
			return fmt.Errorf("encrypted allowlist not found at %s\nRun 'sandclaude firewall-reload' to create it", encPath)
		}
		// Make sure the file is world-readable so proxyuser can read it
		os.Chmod(encPath, 0644)
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/allowed-domains.txt.enc:ro", encPath))
	}

	// Claude auth: in proxy mode, generate dummy token; otherwise let Claude handle auth
	if sc.proxyEnabled {
		debugln("Generating dummy auth token for proxy mode (proxy will inject real credentials)")
		dummyToken := "sk-ant-oat01-" + strings.Repeat("0", 86) + "-" + strings.Repeat("0", 8)
		args = append(args, "-e", fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", dummyToken))
	}
	// In non-proxy mode, Claude Code will handle its own authentication

	// Get gh token if available
	ghToken := ""
	cmd := exec.Command("gh", "auth", "token")
	if output, err := cmd.Output(); err == nil {
		ghToken = strings.TrimSpace(string(output))
	}
	if ghToken != "" {
		debugln("GitHub token found, passing GH_TOKEN to container")
		args = append(args, "-e", fmt.Sprintf("GH_TOKEN=%s", ghToken))
	} else {
		debugln("No GitHub token found (gh auth token returned empty)")
	}

	// Mount .claude.json from host (Claude Code config file)
	// Note: We don't mount the entire .claude directory here - that's handled below
	// with granular per-subdirectory mounts to allow merging from multiple sources.
	args = append(args,
		"-v", fmt.Sprintf("%s:/home/claude/.claude.json", filepath.Join(home, ".claude.json")),
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
	)

	// Mount host .gitconfig so commits inside the container are attributed to the host user
	hostGitconfig := filepath.Join(home, ".gitconfig")
	if _, err := os.Stat(hostGitconfig); err == nil {
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/.gitconfig:ro", hostGitconfig))
	}

	// Mount .claude subdirectories from three sources, merging their contents:
	//   1. Host ~/.claude/*          — user's personal configuration
	//   2. Repo .devcontainer/.claude/* — repo-specific configuration
	//   3. Workspace .claude/*       — workspace-specific configuration
	//
	// Strategy:
	// - Read-only subdirs (skills, rules, agents, commands): merge from all sources, mount :ro
	// - Writable subdirs (projects, sessions, file-history, etc.): mount from host :rw for persistence
	// - Repo and workspace provide read-only configuration
	// - Host provides writable state/data

	// Track mounted container destinations to avoid duplicate mounts (docker rejects them).
	mountedDsts := make(map[string]bool)

	// Helper function to mount individual items from a .claude subdirectory (read-only).
	// Skips any destination already mounted by a higher-priority source.
	mountClaudeSubdirItems := func(sourceLabel, sourceClaudeDir, subName string) {
		srcSubDir := filepath.Join(sourceClaudeDir, subName)
		if entries, err := os.ReadDir(srcSubDir); err == nil {
			debugf("Mounting %s .claude/%s/* (%d items, read-only)", sourceLabel, subName, len(entries))
			for _, entry := range entries {
				entrySrc := filepath.Join(srcSubDir, entry.Name())
				entryDst := fmt.Sprintf("/home/claude/.claude/%s/%s", subName, entry.Name())
				if mountedDsts[entryDst] {
					debugf("  skip (already mounted): %s", entryDst)
					continue
				}
				mountedDsts[entryDst] = true
				debugf("  volume: %s -> %s:ro", entrySrc, entryDst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", entrySrc, entryDst))
			}
		}
	}

	// Categorize .claude subdirectories by their access patterns
	readOnlyMergeableSubdirs := map[string]bool{
		"skills":   true,
		"rules":    true,
		"agents":   true,
		"commands": true,
	}

	// Subdirectories that need write access for Claude Code to persist data
	writableSubdirs := map[string]bool{
		"projects":        true,
		"sessions":        true,
		"file-history":    true,
		"backups":         true,
		"cache":           true,
		"shell-snapshots": true,
		"todos":           true,
		"tasks":           true,
		"telemetry":       true,
		"paste-cache":     true,
		"session-env":     true,
		"statsig":         true,
		"plugins":         true,
		"ide":             true,
	}

	// Collect all .claude subdirectories that exist across all sources
	allSubdirs := make(map[string]bool)

	hostClaudeDir := filepath.Join(home, ".claude")
	if entries, err := os.ReadDir(hostClaudeDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				allSubdirs[e.Name()] = true
			}
		}
	}

	repoClaudeDir := filepath.Join(assetsDir(), ".claude")
	if entries, err := os.ReadDir(repoClaudeDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				allSubdirs[e.Name()] = true
			}
		}
	}

	workspaceClaudeDir := filepath.Join(workspace, ".claude")
	workspaceSandclaudeDir := filepath.Join(workspace, ".sandclaude")
	if entries, err := os.ReadDir(workspaceClaudeDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				allSubdirs[e.Name()] = true
			}
		}
	}

	// Mount each subdirectory based on its category
	for subName := range allSubdirs {
		if readOnlyMergeableSubdirs[subName] {
			// Read-only mergeable subdirectories: mount individual items from all three sources
			mountClaudeSubdirItems("host", hostClaudeDir, subName)
			mountClaudeSubdirItems("repo", repoClaudeDir, subName)
			mountClaudeSubdirItems("workspace", workspaceClaudeDir, subName)

			// Also check .sandclaude for project-specific items (primarily for skills)
			if subName == "skills" {
				mountClaudeSubdirItems("workspace/.sandclaude", workspaceSandclaudeDir, subName)
			}
		} else if writableSubdirs[subName] {
			// Writable subdirectories: mount from host with read-write access for persistence
			hostSubdir := filepath.Join(hostClaudeDir, subName)
			if _, err := os.Stat(hostSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				debugf("volume: host .claude/%s -> %s:rw", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:rw", hostSubdir, dst))
			}
		} else {
			// Other subdirectories: mount read-only from workspace if it exists, otherwise from host
			workspaceSubdir := filepath.Join(workspaceClaudeDir, subName)
			hostSubdir := filepath.Join(hostClaudeDir, subName)

			if _, err := os.Stat(workspaceSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				debugf("volume: workspace .claude/%s -> %s:ro", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", workspaceSubdir, dst))
			} else if _, err := os.Stat(hostSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				debugf("volume: host .claude/%s -> %s:ro", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", hostSubdir, dst))
			}
		}
	}

	// Mount empty tmpfs over .devcontainer to hide it from the container (unless --keep-devfiles)
	if !keepDevfiles {
		args = append(args, "--tmpfs", fmt.Sprintf("%s/.devcontainer:rw,noexec,nosuid,size=1m", workspace))
	}

	// The plaintext allowlist always lives at <cwd>/.sandclaude/allowed-domains.txt,
	// owned by the project. The container appends newly-seen domains here in
	// passthrough-and-write mode.
	allowlistPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt")
	if sc.passthroughFirewallAndWrite {
		// Ensure the file exists and is world-writable before the container starts,
		// so proxyuser (which doesn't own the file) can append to it.
		// Must create parent dirs first — OpenFile won't create them, and if the
		// source path doesn't exist Docker will bind-mount it as a directory.
		if err := os.MkdirAll(filepath.Dir(allowlistPath), 0755); err != nil {
			return fmt.Errorf("failed to create allowlist directory: %w", err)
		}
		if f, err := os.OpenFile(allowlistPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err != nil {
			return fmt.Errorf("failed to create allowlist file %s: %w", allowlistPath, err)
		} else {
			f.Close()
		}
		if err := os.Chmod(allowlistPath, 0666); err != nil {
			return fmt.Errorf("failed to chmod allowlist file: %w", err)
		}
	}
	args = append(args, "-v", fmt.Sprintf("%s:/home/claude/allowed-domains.txt:rw", allowlistPath))

	// Selective-mitm inputs, materialized from config for the in-container proxy:
	//  - monitor-list: a plaintext host file (hosts aren't secret, unlike the
	//    allowlist) mounted in; entrypoint passes it to --monitorlist. Absent/empty
	//    config => no file mounted => proxy monitors all allowed hosts (default).
	//  - mitm-ports: passed as an env var the entrypoint forwards to --mitm-ports.
	if cfg, err := readConfig(getProjectDir()); err == nil {
		if len(cfg.MonitorHosts) > 0 {
			monitorPath := monitorHostsPath()
			if err := writeMonitorHostsFile(monitorPath, cfg.MonitorHosts); err != nil {
				return fmt.Errorf("failed to write monitor-hosts file: %w", err)
			}
			args = append(args, "-v", fmt.Sprintf("%s:/home/claude/monitor-hosts.txt:rw", monitorPath))
		}
		args = append(args, "-e", "SANDCLAUDE_MITM_PORTS="+strings.Join(cfg.mitmPortsOrDefault(), ","))
	}

	logsDir := getLogsDir()
	os.MkdirAll(logsDir, 0755)
	args = append(args, "-v", fmt.Sprintf("%s:/home/claude/logs", logsDir))

	// Mount bin/ from the asset bundle so scripts can be edited without rebuilding
	binDir := filepath.Join(assetsDir(), "bin")
	if _, err := os.Stat(binDir); err == nil {
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/bin", binDir))
	}

	// Enable proxy if it was started
	if sc.proxyEnabled {
		args = append(args,
			// Force host.docker.internal to resolve to IPv4 gateway.
			// Without this, Docker Desktop on Mac may set host.docker.internal
			// to an IPv6-only address, which mitmproxy (bound to 0.0.0.0) won't
			// answer, causing immediate connection refused before anything hits mitm.
			"--add-host=host.docker.internal:host-gateway",
			"-e", fmt.Sprintf("HTTP_PROXY=http://host.docker.internal:%s", sc.proxyPort),
			"-e", fmt.Sprintf("HTTPS_PROXY=http://host.docker.internal:%s", sc.proxyPort),
		)

		// Mount just the mitmproxy CA cert (public cert, not a secret)
		mitmDir := filepath.Join(home, ".mitmproxy")
		os.MkdirAll(mitmDir, 0755)
		certPath := filepath.Join(mitmDir, "mitmproxy-ca-cert.pem")
		if _, err := os.Stat(certPath); err == nil {
			args = append(args, "-v", fmt.Sprintf("%s:/home/claude/.mitmproxy/mitmproxy-ca-cert.pem:ro", certPath))
			debugln("Proxy CA cert found on host, mounting into container")
		} else {
			debugln("Proxy CA cert not on host — will be generated when proxy starts")
		}
	}

	// Mark the container as a sandclaude environment so nested `sandclaude start`
	// calls can detect they're already inside and skip the docker layer entirely.
	args = append(args, "-e", "SANDCLAUDE_CONTAINER=1")

	// DinD: signal entrypoint to start inner dockerd and expose ports
	if sc.dindEnabled {
		args = append(args, "-e", "DIND_ENABLED=1")

		// Use a named volume for the inner Docker data root.
		// Bind mounts fail with "lchown /proc: permission denied" when Docker
		// tries to extract image layers that contain /proc entries — named
		// volumes avoid this because Docker manages ownership internally.
		dindVol := dindVolumeName(cfg.Workspace)
		args = append(args, "-v", fmt.Sprintf("%s:/var/lib/docker-dind:rw", dindVol))

		for _, port := range sc.dindPorts {
			args = append(args, "-p", port)
		}
		if len(sc.dindPorts) > 0 {
			log.Printf("DinD enabled (ports: %s, volume: %s)", strings.Join(sc.dindPorts, ", "), dindVol)
		} else {
			log.Printf("DinD enabled (volume: %s)", dindVol)
		}
	}

	if cfg.LaunchTmux {
		args = append(args, "-e", "LAUNCH_TMUX=1")
		log.Println("tmux launch enabled")
	}

	args = append(args, imageName)

	debugf("Docker command: docker %s", strings.Join(args, " "))

	// `dev` launches the container in a detached host-level tmux session for closed-loop
	// development (observe/drive via capture/send/attach). Plain `start` always runs
	// interactively, attached to the current terminal.
	if sc.devMode {
		// Keep -it: the host tmux session provides a real PTY (the container sees
		// /dev/pts/0), so the interactive container — including LAUNCH_TMUX=1's inner
		// tmux attach — works exactly as in the attached path. capture/send/attach then
		// drive it via the host session.
		return sc.startDetached(containerName, args)
	}

	dockerCmd := exec.Command("docker", args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	return dockerCmd.Run()
}

// startDetached launches the container (with -it) in a detached host-level tmux session
// for closed-loop development (the `dev` command). The host tmux owns the PTY, so the
// interactive container behaves identically to the attached path; capture/send/attach
// then observe and drive the inner Claude.
func (sc *SandClaude) startDetached(containerName string, args []string) error {
	sessionName := tmuxSessionNameForContainer(containerName)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		log.Printf("Killing existing tmux session '%s'", sessionName)
		if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil {
			return fmt.Errorf("failed to kill existing tmux session '%s': %w", sessionName, err)
		}
	}

	parts := append([]string{"docker"}, args...)
	dockerCmdStr := buildShellCommand(parts)
	debugf("Detached tmux command: tmux new-session -d -s %s %q", sessionName, dockerCmdStr)

	if err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, dockerCmdStr).Run(); err != nil {
		return fmt.Errorf("failed to create tmux session '%s': %w\n\nIs tmux installed?", sessionName, err)
	}

	sc.detachedSession = sessionName

	fmt.Printf("\nsandclaude started in tmux session: %s\n\n", sessionName)
	fmt.Printf("  sandclaude capture          # read inner Claude output\n")
	fmt.Printf("  sandclaude send '<prompt>'  # send a prompt to inner Claude\n")
	fmt.Printf("  sandclaude attach           # attach interactively\n")
	fmt.Printf("  docker ps --filter name=%s\n\n", containerName)
	return nil
}

// ensureImage builds the Docker image if it doesn't exist
func (sc *SandClaude) ensureImage(imageName string) error {
	// Check if image exists
	cmd := exec.Command("docker", "image", "inspect", imageName)
	if err := cmd.Run(); err == nil {
		debugf("Image '%s' already exists, skipping build", imageName)
		return nil // Image exists
	}

	// Build image
	log.Printf("Building %s image...", imageName)

	// Get user and group IDs
	cmd = exec.Command("id", "-u")
	userIDBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get user ID: %w", err)
	}
	userID := strings.TrimSpace(string(userIDBytes))

	cmd = exec.Command("id", "-g")
	groupIDBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get group ID: %w", err)
	}
	groupID := strings.TrimSpace(string(groupIDBytes))

	// Build arg list: always set proxy and user IDs.
	buildArgs := []string{
		"build",
		"--build-arg", fmt.Sprintf("USER_ID=%s", userID),
		"--build-arg", fmt.Sprintf("GROUP_ID=%s", groupID),
		// Override proxy for build containers: the shell's HTTPS_PROXY points to
		// 127.0.0.1:3128 (allowlist proxy), but inside a DinD build container that
		// address is unreachable. 172.18.0.1:3128 is the gateway IP that build
		// containers can actually reach.
		"--build-arg", "HTTP_PROXY=http://172.18.0.1:3128",
		"--build-arg", "HTTPS_PROXY=http://172.18.0.1:3128",
		"--build-arg", "NO_PROXY=172.18.0.0/16,127.0.0.0/8",
		// Skip the ~200MB Chromium binary download when the upstream proxy cannot
		// stream large response bodies (mitmproxy buffers the whole file, which OOMs
		// or times out). The browser can be installed separately after the image is
		// running if needed.
		"--build-arg", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
	}

	// The allowlist proxy does TLS interception (MITM), so build containers must
	// trust its CA cert for HTTPS to work. Read the cert and pass it base64-encoded.
	if certBytes, err := os.ReadFile("/etc/proxy-ca.crt"); err == nil {
		buildArgs = append(buildArgs,
			"--build-arg", fmt.Sprintf("PROXY_CA_CERT=%s", base64.StdEncoding.EncodeToString(certBytes)),
		)
	}

	buildArgs = append(buildArgs, "-t", imageName, assetsDir())
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
}

// startDirect launches Claude directly inside the current sandclaude container.
// Used when sandclaude start is called from within a running sandclaude container
// (detected via SANDCLAUDE_CONTAINER=1). The proxy, firewall, and workspace are
// already set up by the outer entrypoint.
//
// We run claude directly rather than via launcher.py to avoid launcher.py trying to
// create a tmux session named "sandclaude" which already exists (it's the outer session).
func (sc *SandClaude) startDirect(cfg *ProjectConfig) error {
	log.Println("Running inside sandclaude container — starting Claude directly (no nested Docker)")

	// patch-claude-settings.py must run before Claude starts.
	patch := exec.Command("python3", "/home/claude/bin/patch-claude-settings.py")
	patch.Stdout = os.Stdout
	patch.Stderr = os.Stderr
	if err := patch.Run(); err != nil {
		log.Printf("Warning: patch-claude-settings.py failed: %v", err)
	}

	if !sc.devMode {
		cmd := exec.Command("claude", "--dangerously-skip-permissions")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if cfg.Workspace != "" {
			cmd.Dir = cfg.Workspace
		}
		return cmd.Run()
	}

	// dev: create a new detached tmux session and run claude directly inside it.
	// We avoid launcher.py because it would try to create a session named "sandclaude"
	// which already exists (the outer session we're running in).
	containerName := containerNameForWorkspace(cfg.Workspace)
	sessionName := tmuxSessionNameForContainer(containerName)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		log.Printf("Killing existing tmux session '%s'", sessionName)
		if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil {
			return fmt.Errorf("failed to kill existing tmux session '%s': %w", sessionName, err)
		}
	}

	claudeCmd := "cd " + shellQuote(cfg.Workspace) + " && claude --dangerously-skip-permissions; exec bash"
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, claudeCmd).Run(); err != nil {
		return fmt.Errorf("failed to create tmux session '%s': %w", sessionName, err)
	}

	sc.detachedSession = sessionName

	fmt.Printf("\nClaude started in tmux session: %s\n\n", sessionName)
	fmt.Printf("  sandclaude capture          # read inner Claude output\n")
	fmt.Printf("  sandclaude send '<prompt>'  # send a prompt to inner Claude\n")
	fmt.Printf("  sandclaude attach           # attach interactively\n\n")
	return nil
}

// Run starts the full sandclaude environment
func (sc *SandClaude) Run(keepDevfiles bool) error {
	log.Println("SandClaude - Secure Claude Code Environment")

	projectDir := getProjectDir()
	cfg, err := readConfig(projectDir)
	if err != nil {
		return err
	}

	debugf("Config: workspace=%s proxy=%v dind=%v", cfg.Workspace, cfg.ProxyEnabled, cfg.DindEnabled)

	if _, err := os.Stat(cfg.Workspace); os.IsNotExist(err) {
		return fmt.Errorf("workspace not found: %s", cfg.Workspace)
	}

	if err := registerProject(cfg.Workspace); err != nil {
		debugf("Warning: failed to update project registry: %v", err)
	}

	// If we're already inside a sandclaude container, skip docker entirely.
	// The proxy, firewall, and workspace are set up by the outer entrypoint.
	if os.Getenv("SANDCLAUDE_CONTAINER") == "1" {
		return sc.startDirect(cfg)
	}

	// Check if DinD is enabled (unless --disable-dind was passed)
	if !sc.disableDind && cfg.DindEnabled {
		sc.dindEnabled = true
		sc.dindPorts = cfg.DindPorts
	}

	// Re-encrypt the allowlist from plaintext so the mounted .enc always reflects
	// the current allowed-domains.txt. Without this, editing the plaintext and then
	// running start/dev silently launches with a stale .enc (the proxy reads the
	// encrypted file, not the plaintext), so newly-added domains stay blocked.
	if cfg.ProxyEnabled {
		if err := syncEncryptedAllowlist(); err != nil {
			log.Printf("Warning: could not re-encrypt allowlist, using existing .enc: %v", err)
		}
	}

	// Build the image before starting the proxy — the proxy intercepts HTTPS during
	// docker build and presents its own cert, which the build's curl won't trust yet.
	if err := sc.ensureImage("sandclaude-stable"); err != nil {
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
		if state, derr := readDashboardState(); derr == nil && state != nil {
			url := fmt.Sprintf("http://127.0.0.1:%d/p/%s?token=%s", state.Port, projectID(cfg.Workspace), state.Token)
			log.Printf("Open in dashboard: %s", url)
			if oerr := openBrowser(url); oerr != nil {
				debugf("failed to open browser: %v", oerr)
			}
		}
	}

	return err
}

// cmdUpdate updates project config fields without touching credentials or the allowlist key.
func cmdUpdate() error {
	projectDir := getProjectDir()
	cfg, err := readConfig(projectDir)
	if err != nil {
		return err
	}

	log.Println("Updating project config (press Enter to keep current value)")
	log.Println()

	reader := bufio.NewReader(os.Stdin)

	// Docker-in-Docker
	dindPrompt := "n"
	if cfg.DindEnabled {
		dindPrompt = "Y"
	}
	fmt.Printf("Enable Docker-in-Docker? (current: %s) [y/N]: ", dindPrompt)
	dindInput, _ := reader.ReadString('\n')
	dindInput = strings.TrimSpace(strings.ToLower(dindInput))
	if dindInput == "" {
		log.Printf("  DinD unchanged: %v\n", cfg.DindEnabled)
	} else {
		cfg.DindEnabled = dindInput == "y" || dindInput == "yes"
		log.Printf("  DinD enabled: %v\n", cfg.DindEnabled)
		if !cfg.DindEnabled {
			cfg.DindPorts = nil
		}
	}

	if cfg.DindEnabled {
		currentPorts := strings.Join(cfg.DindPorts, ",")
		if currentPorts == "" {
			currentPorts = "none"
		}
		fmt.Printf("  Port mappings to expose to host (current: %s, blank to keep, 'none' to clear): ", currentPorts)
		portsInput, _ := reader.ReadString('\n')
		portsInput = strings.TrimSpace(portsInput)
		if portsInput == "" {
			log.Printf("  DinD ports unchanged: %s\n", currentPorts)
		} else if portsInput == "none" {
			cfg.DindPorts = nil
			log.Println("  DinD ports cleared — inner containers accessible to Claude only")
		} else {
			cfg.DindPorts = strings.FieldsFunc(portsInput, func(r rune) bool {
				return r == ',' || r == ' '
			})
			log.Printf("  DinD ports set to: %s\n", strings.Join(cfg.DindPorts, ", "))
		}
	}

	log.Println()

	// tmux
	tmuxPrompt := "n"
	if cfg.LaunchTmux {
		tmuxPrompt = "Y"
	}
	fmt.Printf("Launch with tmux? (current: %s) [y/N]: ", tmuxPrompt)
	tmuxInput, _ := reader.ReadString('\n')
	tmuxInput = strings.TrimSpace(strings.ToLower(tmuxInput))
	if tmuxInput != "" {
		cfg.LaunchTmux = tmuxInput == "y" || tmuxInput == "yes"
		log.Printf("  Launch with tmux: %v\n", cfg.LaunchTmux)
	} else {
		log.Printf("  Launch with tmux unchanged: %v\n", cfg.LaunchTmux)
	}

	log.Println()

	// Workspace
	fmt.Printf("Workspace directory (current: %s, blank to keep): ", cfg.Workspace)
	wsInput, _ := reader.ReadString('\n')
	wsInput = strings.TrimSpace(wsInput)
	if wsInput == "" {
		log.Printf("  Workspace unchanged: %s\n", cfg.Workspace)
	} else {
		cfg.Workspace = wsInput
		log.Printf("  Workspace set to: %s\n", wsInput)
		if _, err := os.Stat(wsInput); os.IsNotExist(err) {
			if askYesNo("Workspace doesn't exist. Create it?") {
				os.MkdirAll(wsInput, 0755)
				log.Printf("  Created workspace: %s\n", wsInput)
			}
		}
	}

	// Inherit cross-project defaults (monitor-list, mitm-ports) set in the global
	// settings, so a new project starts with the selective-mitm policy you've
	// chosen once. Existing projects are never touched by global defaults.
	if def := readGlobalDefaults(); len(def.MonitorHosts) > 0 || len(def.MitmPorts) > 0 {
		if len(cfg.MonitorHosts) == 0 {
			cfg.MonitorHosts = def.MonitorHosts
		}
		if len(cfg.MitmPorts) == 0 {
			cfg.MitmPorts = def.MitmPorts
		}
		if len(def.MonitorHosts) > 0 || len(def.MitmPorts) > 0 {
			log.Println("  Inherited selective-mitm defaults from global settings")
		}
	}

	if err := writeConfig(projectDir, cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	log.Println()
	log.Println("✅ Config updated (credentials and allowlist key unchanged)")
	return nil
}

// cmdInit initializes the ./project/ structure
func cmdInit() error {
	projectDir := getProjectDir()

	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("project already initialized at %s\n   To update config run: sandclaude update\n   To remove it run:     sandclaude remove", projectDir)
	}

	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return fmt.Errorf("failed to create project dir: %w", err)
	}

	log.Printf("Initializing project at: %s\n", projectDir)
	log.Println()

	cfg := &ProjectConfig{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	log.Println()

	// Credential proxy
	if askYesNo("RECOMMENDED: Enable credential proxy (hides secrets from Claude)?") {
		cfg.ProxyEnabled = true
		log.Println("✅ Proxy mode enabled")
		log.Println()
		log.Println("⚠️  IMPORTANT: You must configure real credentials before starting!")
		log.Printf("   Global credentials live at: %s\n", globalCredentialsPath())
		log.Println("   Run: sandclaude populate-proxy-credentials")
		log.Println("   For a project-specific override, run: sandclaude populate-proxy-credentials --project")
	}

	log.Println()

	// tmux
	if askYesNo("Launch with tmux?") {
		cfg.LaunchTmux = true
		log.Println("✅ tmux launch enabled")
	}

	log.Println()

	// Docker-in-Docker
	if askYesNo("Enable Docker-in-Docker (inner containers, Claude-accessible)?") {
		cfg.DindEnabled = true
		log.Println("Docker-in-Docker enabled — Claude can start inner containers")
		log.Println()
		log.Println("   Inner containers' network egress goes through the allowlist proxy")

		reader2 := bufio.NewReader(os.Stdin)
		fmt.Print("Port mappings to expose to host (e.g. 3000:3000,8000:8000, blank for none): ")
		portsInput, _ := reader2.ReadString('\n')
		portsInput = strings.TrimSpace(portsInput)
		if portsInput != "" {
			ports := strings.FieldsFunc(portsInput, func(r rune) bool {
				return r == ',' || r == ' '
			})
			cfg.DindPorts = ports
			log.Printf("   Port mappings: %s\n", strings.Join(ports, ", "))
		} else {
			log.Println("   No host port mappings — inner containers accessible to Claude only")
		}
	} else {
		log.Println("Docker-in-Docker disabled")
	}

	log.Println()

	// Workspace directory. sandclaude now runs from the project root itself (not from
	// an embedded .devcontainer), so the workspace defaults to the current directory.
	cwd, _ := os.Getwd()
	defaultWorkspace := cwd
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Workspace directory (default: %s): ", defaultWorkspace)
	workspaceInput, _ := reader.ReadString('\n')
	workspace := strings.TrimSpace(workspaceInput)
	if workspace == "" {
		workspace = defaultWorkspace
	}
	cfg.Workspace = workspace

	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		if askYesNo("Workspace doesn't exist. Create it?") {
			os.MkdirAll(workspace, 0755)
			log.Printf("Created workspace: %s\n", workspace)
		}
	}

	if err := writeConfig(projectDir, cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Generate encryption key for allowlist
	keyPath := filepath.Join(projectDir, ".allowlist-key")
	keyData := make([]byte, 32)
	if _, err := rand.Read(keyData); err != nil {
		return fmt.Errorf("failed to generate encryption key: %w", err)
	}
	// Store as hex string
	keyHex := fmt.Sprintf("%x", keyData)
	if err := os.WriteFile(keyPath, []byte(keyHex), 0600); err != nil {
		return fmt.Errorf("failed to write encryption key: %w", err)
	}
	log.Println("✅ Encryption key generated")

	// Credentials are global now — no per-project template is created during init.
	// Populate them with: sandclaude populate-proxy-credentials
	// (or, for a project-specific override: sandclaude populate-proxy-credentials --project)

	// Seed the project allowlist from the shipped defaults, then encrypt it.
	log.Println()
	log.Println("Encrypting allowlist...")
	seedPath := filepath.Join(assetsDir(), "allowlist-proxy", "allowed-domains.txt")
	plaintextPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt")
	encPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt.enc")

	// Copy the seed into the project if the project doesn't already have one.
	if _, err := os.Stat(plaintextPath); os.IsNotExist(err) {
		seed, err := os.ReadFile(seedPath)
		if err != nil {
			return fmt.Errorf("read allowlist seed %s: %w\n\nIs sandclaude installed? Run install.sh", seedPath, err)
		}
		if err := os.WriteFile(plaintextPath, seed, 0644); err != nil {
			return fmt.Errorf("write %s: %w", plaintextPath, err)
		}
		log.Printf("✅ Allowlist seeded at: %s\n", plaintextPath)
	} else {
		log.Printf("✅ Allowlist already exists at: %s (not overwriting)\n", plaintextPath)
	}

	plaintext, err := os.ReadFile(plaintextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plaintextPath, err)
	}

	key, err := allowlistDeriveKey(keyHex)
	if err != nil {
		return err
	}

	ciphertext, err := allowlistEncrypt(key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	if err := os.WriteFile(encPath, ciphertext, 0644); err != nil {
		return fmt.Errorf("write %s: %w", encPath, err)
	}
	log.Printf("✅ Allowlist encrypted\n")

	log.Println()
	log.Printf("✅ Project initialized at: %s\n", projectDir)
	log.Printf("   Config: %s/config.json\n", projectDir)
	log.Printf("   Encryption key: %s/.allowlist-key (DO NOT commit)\n", projectDir)
	log.Println()
	log.Println("Next steps:")
	if cfg.ProxyEnabled {
		log.Println("  sandclaude populate-proxy-credentials")
	}
	log.Println("  sandclaude start")
	log.Println()

	// Ensure .sandclaude/ (this project's config, key, and logs) is gitignored.
	ensureGitignored(".sandclaude/")

	return nil
}

// ensureGitignored adds entry to <cwd>/.gitignore if not already present.
func ensureGitignored(entry string) {
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

// cmdStart starts Claude Code interactively, attached to the current terminal.
// cmdStart launches Claude Code detached and opens the dashboard to this
// project, so the browser is where you interact — your shell prompt returns
// immediately. Pass --foreground for the classic behavior of running attached to
// the current terminal instead.
func cmdStart(args []string) error {
	foreground := false
	for _, arg := range args {
		if arg == "--foreground" {
			foreground = true
		}
	}
	// Detached by default (like dev), unless --foreground was asked for.
	return runStart(args, !foreground)
}

// cmdDev starts Claude Code in a detached host tmux session for closed-loop development:
// the container runs in the background and is observed/driven via capture/send/attach.
func cmdDev(args []string) error {
	return runStart(args, true)
}

func runStart(args []string, devMode bool) error {
	disableFirewall := false
	passthroughFirewallAndWrite := false
	disableDind := false
	keepDevfiles := false

	for _, arg := range args {
		switch arg {
		case "--disable-firewall":
			disableFirewall = true
		case "--passthrough-firewall-and-write":
			passthroughFirewallAndWrite = true
		case "--disable-dind":
			disableDind = true
		case "--keep-devfiles":
			keepDevfiles = true
		case "--foreground", "--debug":
			// --foreground handled in cmdStart; --debug handled globally in main()
		}
	}

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	sc.disableFirewall = disableFirewall
	sc.passthroughFirewallAndWrite = passthroughFirewallAndWrite
	sc.disableDind = disableDind
	sc.devMode = devMode
	return sc.Run(keepDevfiles)
}

// cmdList shows the ./project/config.json
func cmdList() error {
	projectDir := getProjectDir()

	cfg, err := readConfig(projectDir)
	if err != nil {
		log.Println("No project configured. Run: sandclaude init")
		return nil
	}

	log.Println("Project configuration:")
	log.Println()
	log.Printf("  Config:    %s/config.json\n", projectDir)
	log.Printf("  Workspace: %s\n", cfg.Workspace)
	if cfg.DindEnabled {
		log.Print("  DinD:      enabled")
		if len(cfg.DindPorts) > 0 {
			log.Printf(" (ports: %s)", strings.Join(cfg.DindPorts, ", "))
		}
		log.Println()
		log.Printf("  DinD data: docker volume %s\n", dindVolumeName(cfg.Workspace))
	}
	if cfg.ProxyEnabled {
		log.Println("  Proxy:     enabled")
	}
	log.Println()

	return nil
}

// cmdFirewallMonitor tails the allowlist proxy log inside the running container
func cmdFirewallMonitor() error {
	containerName := runningContainerName()

	// Verify container is running.
	if out, err := exec.Command("docker", "inspect", "--format={{.Id}}", containerName).Output(); err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("no running container found (expected container name: '%s')", containerName)
	}

	// less +F: tails live; Ctrl+C to stop following and search, F to resume
	dockerCmd := exec.Command("docker", "exec", "-it", containerName,
		"less", "+F", "/home/claude/logs/proxy.log")
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	return dockerCmd.Run()
}

// cmdShell opens a bash shell in the container
func cmdShell() error {
	projectDir := getProjectDir()

	workspace := projectDir // fallback
	if cfg, err := readConfig(projectDir); err == nil {
		workspace = cfg.Workspace
	}

	// Ensure image exists
	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	imageName := "sandclaude-stable"
	if err := sc.ensureImage(imageName); err != nil {
		return err
	}

	// Get gh token if available
	ghToken := ""
	cmd := exec.Command("gh", "auth", "token")
	if output, err := cmd.Output(); err == nil {
		ghToken = strings.TrimSpace(string(output))
	}

	args := []string{
		"run", "--rm", "-it",
		"--cap-add=NET_ADMIN",
		"--cap-add=NET_RAW",
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
		"--entrypoint", "/bin/bash",
	}

	if ghToken != "" {
		args = append(args, "-e", fmt.Sprintf("GH_TOKEN=%s", ghToken))
	}

	args = append(args, imageName)

	dockerCmd := exec.Command("docker", args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	return dockerCmd.Run()
}

// cmdRebuild rebuilds the Docker image, optionally destroying the existing image/container and/or inner docker data first
func cmdRebuild(destroy bool, destroyInner bool) error {
	if destroyInner {
		projectDir := getProjectDir()
		cfg, cfgErr := readConfig(projectDir)
		if cfgErr != nil {
			log.Printf("Warning: could not read config to derive DinD volume name: %v", cfgErr)
		} else {
			volName := dindVolumeName(cfg.Workspace)
			log.Printf("Removing inner docker volume %s...", volName)
			rmCmd := exec.Command("docker", "volume", "rm", volName)
			rmCmd.Stdout = os.Stdout
			rmCmd.Stderr = os.Stderr
			if err := rmCmd.Run(); err != nil {
				log.Printf("Warning: could not remove volume %s (may not exist yet): %v", volName, err)
			} else {
				log.Println("✅ Inner docker volume removed (images and volumes wiped)")
			}
		}
		// Remove legacy bind-mount directory if it survived from an older install.
		legacyDir := filepath.Join(getProjectDir(), "dind-data")
		if _, err := os.Stat(legacyDir); err == nil {
			log.Printf("Removing legacy DinD data directory %s...", legacyDir)
			os.RemoveAll(legacyDir)
		}
	}

	if destroy {
		log.Println("Destroying existing sandclaude container and image...")

		// Stop and remove any running container
		stopCmd := exec.Command("docker", "rm", "-f", "sandclaude-stable")
		stopCmd.Stdout = os.Stdout
		stopCmd.Stderr = os.Stderr
		stopCmd.Run() // ignore error — container may not exist

		// Remove the image
		rmiCmd := exec.Command("docker", "rmi", "-f", "sandclaude-stable")
		rmiCmd.Stdout = os.Stdout
		rmiCmd.Stderr = os.Stderr
		rmiCmd.Run() // ignore error — image may not exist
	}

	log.Println("Building sandclaude image...")

	// Get user and group IDs
	cmd := exec.Command("id", "-u")
	userIDBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get user ID: %w", err)
	}
	userID := strings.TrimSpace(string(userIDBytes))

	cmd = exec.Command("id", "-g")
	groupIDBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get group ID: %w", err)
	}
	groupID := strings.TrimSpace(string(groupIDBytes))

	buildArgs := []string{"build",
		"--build-arg", fmt.Sprintf("USER_ID=%s", userID),
		"--build-arg", fmt.Sprintf("GROUP_ID=%s", groupID),
	}
	if destroy {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, "-t", "sandclaude-stable", assetsDir())
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
}

// globalCredentialsPath returns the shared, cross-project credentials file
// (~/.sandclaude/proxy-credentials.json).
func globalCredentialsPath() string {
	return filepath.Join(sandclaudeHome(), "proxy-credentials.json")
}

// projectCredentialsPath returns the per-project credentials override
// (<cwd>/.sandclaude/project/proxy-credentials.json).
func projectCredentialsPath() string {
	return filepath.Join(getProjectDir(), "proxy-credentials.json")
}

// loadCredsMap reads a proxy-credentials.json file into a domain->entry map.
// Returns an empty map (not an error) when the file is absent.
func loadCredsMap(path string) (map[string]map[string]string, error) {
	creds := map[string]map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return creds, nil
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return creds, nil
}

// resolveCredentialsFile returns a best-effort credentials path WITHOUT creating a
// temp file — used at construction time (NewSandClaude) where no lifecycle owner
// exists to clean up. It prefers the global file, falling back to the project file.
// startProxy re-resolves via resolveCredentialsFileTracked, which performs the real
// per-domain merge and records the temp file for cleanup on stopProxy.
func resolveCredentialsFile() string {
	if _, err := os.Stat(globalCredentialsPath()); err == nil {
		return globalCredentialsPath()
	}
	if _, err := os.Stat(projectCredentialsPath()); err == nil {
		return projectCredentialsPath()
	}
	return globalCredentialsPath()
}

// resolveCredentialsFileTracked is like resolveCredentialsFile but also returns the
// path of any temp file it created (empty string if it returned a real file directly),
// so the caller can delete it on shutdown.
func resolveCredentialsFileTracked() (credsFile string, tempFile string) {
	globalPath := globalCredentialsPath()
	projectPath := projectCredentialsPath()

	_, globalErr := os.Stat(globalPath)
	_, projectErr := os.Stat(projectPath)
	globalExists := globalErr == nil
	projectExists := projectErr == nil

	switch {
	case globalExists && !projectExists:
		return globalPath, ""
	case !globalExists && projectExists:
		return projectPath, ""
	case !globalExists && !projectExists:
		// Neither exists — return the global path so downstream "file not found"
		// messaging points at the canonical location.
		return globalPath, ""
	}

	// Both exist: merge, project wins per-domain.
	global, err := loadCredsMap(globalPath)
	if err != nil {
		log.Printf("Warning: %v — falling back to project credentials only", err)
		return projectPath, ""
	}
	project, err := loadCredsMap(projectPath)
	if err != nil {
		log.Printf("Warning: %v — falling back to global credentials only", err)
		return globalPath, ""
	}

	merged := make(map[string]map[string]string, len(global)+len(project))
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v // project overrides/extends
	}

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		log.Printf("Warning: failed to marshal merged credentials: %v — using global only", err)
		return globalPath, ""
	}

	tmp, err := os.CreateTemp("", "sandclaude-merged-creds-*.json")
	if err != nil {
		log.Printf("Warning: failed to create temp credentials file: %v — using global only", err)
		return globalPath, ""
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		log.Printf("Warning: failed to chmod temp credentials file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		log.Printf("Warning: failed to write merged credentials: %v — using global only", err)
		return globalPath, ""
	}
	tmp.Close()

	debugf("Merged %d global + %d project credential entries -> %s", len(global), len(project), tmp.Name())
	return tmp.Name(), tmp.Name()
}

// dummyCredValues is the set of placeholder values written by the cmdInit template.
var dummyCredValues = map[string]bool{
	"Bearer sk-ant-oat01-...":  true,
	"token gho_real_token_here": true,
}

// hasOnlyDummyCredentials returns true when the file doesn't exist, can't be
// parsed, is empty, or every credential value matches a known placeholder.
func hasOnlyDummyCredentials(credsPath string) bool {
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return true
	}
	creds := map[string]map[string]string{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return true
	}
	if len(creds) == 0 {
		return true
	}
	for _, entry := range creds {
		if !dummyCredValues[entry["value"]] {
			return false
		}
	}
	return true
}

// cmdPopulateProxyCredentials populates proxy-credentials.json interactively using
// claude setup-token. By default it writes the global file (~/.sandclaude/proxy-credentials.json)
// shared across all projects; with projectScope=true it writes the per-project override
// (<cwd>/.sandclaude/project/proxy-credentials.json), which takes precedence per-domain.
func cmdPopulateProxyCredentials(projectScope bool) error {
	var credsPath string
	if projectScope {
		projectDir := getProjectDir()
		if _, err := os.Stat(projectDir); os.IsNotExist(err) {
			return fmt.Errorf("no project found — run: sandclaude init")
		}
		credsPath = projectCredentialsPath()
		fmt.Printf("Writing project-specific credentials to: %s\n\n", credsPath)
	} else {
		if err := os.MkdirAll(sandclaudeHome(), 0700); err != nil {
			return fmt.Errorf("failed to create %s: %w", sandclaudeHome(), err)
		}
		credsPath = globalCredentialsPath()
		fmt.Printf("Writing global credentials to: %s\n\n", credsPath)
	}

	// If real credentials already exist, confirm before overwriting
	if !hasOnlyDummyCredentials(credsPath) {
		fmt.Println("⚠️  proxy-credentials.json already contains real credentials.")
		if !askYesNo("Are you sure you want to replace them?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Read existing credentials file if present, otherwise start fresh
	creds := map[string]map[string]string{}
	if data, err := os.ReadFile(credsPath); err == nil {
		if err := json.Unmarshal(data, &creds); err != nil {
			log.Printf("Warning: could not parse existing proxy-credentials.json, starting fresh: %v", err)
			creds = map[string]map[string]string{}
		}
	}

	anyWritten := false

	// Claude credentials
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Println("'claude' not found in PATH — skipping Anthropic credentials")
	} else if askYesNo("Populate Claude credentials?") {
		fmt.Println("Running 'claude setup-token' — follow any browser prompts...")
		cmd := exec.Command("claude", "setup-token")
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("Warning: 'claude setup-token' failed: %v\n", err)
		} else {
			claudeToken := ""
			for _, line := range strings.Split(string(output), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "sk-ant-") {
					claudeToken = line
					break
				}
			}
			if claudeToken == "" {
				fmt.Println("Warning: could not find token in 'claude setup-token' output")
			} else {
				bearerValue := "Bearer " + claudeToken
				for _, domain := range []string{"api.anthropic.com", "platform.claude.com", "mcp-proxy.anthropic.com"} {
					creds[domain] = map[string]string{
						"header": "Authorization",
						"value":  bearerValue,
					}
				}
				fmt.Println("✓ Anthropic credentials set for api.anthropic.com, platform.claude.com, mcp-proxy.anthropic.com")
				anyWritten = true
			}
		}
	}

	fmt.Println()
	fmt.Println()

	// GitHub credentials
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Println("'gh' not found in PATH — skipping GitHub credentials")
	} else if askYesNo("Populate GitHub credentials?") {
		cmd := exec.Command("gh", "auth", "token")
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("Warning: 'gh auth token' failed: %v\n", err)
		} else {
			ghToken := strings.TrimSpace(string(output))
			if ghToken == "" {
				fmt.Println("Warning: 'gh auth token' returned empty output")
			} else {
				creds["api.github.com"] = map[string]string{
					"header": "Authorization",
					"value":  "token " + ghToken,
				}
				fmt.Println("✓ GitHub credentials set for api.github.com")
				anyWritten = true
			}
		}
	}

	fmt.Println()

	if !anyWritten {
		fmt.Println("No credentials written.")
		return nil
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}
	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", credsPath, err)
	}

	fmt.Printf("Credentials written to %s\n", credsPath)
	return nil
}

// cmdRemove removes the <cwd>/.sandclaude/ directory (config, allowlist, logs).
func cmdRemove() error {
	scDir := sandclaudeDir()

	// Check if project exists
	if _, err := os.Stat(getProjectDir()); os.IsNotExist(err) {
		return fmt.Errorf("no project found at %s", getProjectDir())
	}

	// Confirm deletion
	log.Printf("Warning: This will permanently delete the sandclaude directory\n")
	log.Printf("   Location: %s\n", scDir)
	log.Println("   (config, allowlist, encryption key, and logs)")
	log.Println()

	if !askYesNo("Are you sure you want to remove this project?") {
		log.Println("Cancelled")
		return nil
	}

	// Remove the entire .sandclaude directory
	if err := os.RemoveAll(scDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", scDir, err)
	}

	log.Printf("✅ Project removed: %s\n", scDir)
	return nil
}

// ----------------------------------------------------------------------------
// Encryption helpers (must match allowlist-proxy/main.go)
// ----------------------------------------------------------------------------

// allowlistDeriveKey derives a 32-byte AES-256 key from the passphrase.
func allowlistDeriveKey(passphrase string) ([32]byte, error) {
	if passphrase == "" {
		return [32]byte{}, fmt.Errorf("ALLOWLIST_KEY environment variable is not set")
	}
	return sha256.Sum256([]byte(passphrase + ":allowlist-proxy-v1")), nil
}

// allowlistEncrypt encrypts plaintext with AES-256-GCM. Format: nonce || ciphertext.
func allowlistEncrypt(key [32]byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// syncEncryptedAllowlist encrypts <cwd>/.sandclaude/allowed-domains.txt →
// allowed-domains.txt.enc using the key from .sandclaude/project/.allowlist-key.
// It is the single source of truth for keeping the encrypted file in step with the
// plaintext, shared by startup (Run) and the firewall-reload command.
func syncEncryptedAllowlist() error {
	projectDir := getProjectDir()

	keyPath := filepath.Join(projectDir, ".allowlist-key")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read %s: %w\n\nRun 'sandclaude init' first to generate the encryption key", keyPath, err)
	}

	key, err := allowlistDeriveKey(strings.TrimSpace(string(keyData)))
	if err != nil {
		return err
	}

	plaintextPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt")
	encPath := filepath.Join(sandclaudeDir(), "allowed-domains.txt.enc")

	plaintext, err := os.ReadFile(plaintextPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plaintextPath, err)
	}

	ciphertext, err := allowlistEncrypt(key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	if err := os.WriteFile(encPath, ciphertext, 0644); err != nil {
		return fmt.Errorf("write %s: %w", encPath, err)
	}
	log.Printf("Encrypted allowlist written to %s", encPath)
	return nil
}

// cmdFirewallReload encrypts allowed-domains.txt → allowed-domains.txt.enc
// using the key from project/.allowlist-key, and reloads the running proxy.
func cmdFirewallReload() error {
	if err := syncEncryptedAllowlist(); err != nil {
		return err
	}

	// If a container is running, reload the proxy so it picks up the new allowlist.
	containerName := runningContainerName()
	checkCmd := exec.Command("docker", "inspect", "--format={{.State.Running}}", containerName)
	if out, err := checkCmd.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		if err := reloadProxyInContainer(containerName); err != nil {
			log.Printf("Warning: proxy reload failed (proxy may not be running yet): %v", err)
		} else {
			log.Printf("✅ Reloaded allowlist-proxy in container '%s'", containerName)
		}
	} else {
		log.Printf("✅ No running container found — encrypted file ready for next start")
	}

	return nil
}

// containerNameForWorkspace derives the container name sandclaude uses for a given
// workspace path (sandclaude_<workspace-basename>). Shared by every call site that
// needs to name or look up a project's container, so the convention lives in one place.
func containerNameForWorkspace(workspace string) string {
	return "sandclaude_" + filepath.Base(workspace)
}

// tmuxSessionNameForContainer derives the host-level tmux session name for a detached
// dev session from its container name. The session name matches the container name
// verbatim (underscores preserved) to stay consistent with the container naming
// convention.
func tmuxSessionNameForContainer(containerName string) string {
	return containerName
}

// tmuxSessionNameForWorkspace derives the host-level tmux session name for a given
// workspace path directly.
func tmuxSessionNameForWorkspace(workspace string) string {
	return tmuxSessionNameForContainer(containerNameForWorkspace(workspace))
}

// runningContainerName returns the running container name for the current project
// (sandclaude_<workspace-basename>), matching the name used by start/dev. Falls back
// to the legacy bare "sandclaude" if no project config is readable.
func runningContainerName() string {
	if cfg, err := readConfig(getProjectDir()); err == nil && cfg.Workspace != "" {
		return containerNameForWorkspace(cfg.Workspace)
	}
	return "sandclaude"
}

// reloadProxyInContainer makes the running allowlist-proxy pick up the freshly
// re-encrypted allowlist. The proxy reads its allowlist from /tmp/allowed-domains.txt.enc
// (a startup copy proxyuser can read), while the host's edits land on the bind-mounted
// /home/claude/allowed-domains.txt.enc — so we must re-copy that into /tmp before the
// SIGHUP, otherwise the proxy reloads stale content. Both steps run as root: the proxy
// runs as proxyuser, which the default exec user (claude) cannot signal.
func reloadProxyInContainer(containerName string) error {
	// The monitor-hosts file is bind-mounted only when it exists at `docker run`
	// time, so a project that enables the monitor-list for the first time on a
	// running container has no mount to reload from. To make first-enable work
	// live (no restart), docker cp the current host file into the container before
	// SIGHUP. If the host file doesn't exist (monitor-all default), remove any
	// stale copy so the proxy falls back to monitoring all hosts.
	hostMonitor := monitorHostsPath()
	if _, err := os.Stat(hostMonitor); err == nil {
		cp := exec.Command("docker", "cp", hostMonitor, containerName+":/tmp/monitor-hosts.txt")
		cp.Stderr = os.Stderr
		if err := cp.Run(); err != nil {
			return fmt.Errorf("copy monitor-hosts into container: %w", err)
		}
	} else {
		// Best-effort removal of a stale in-container copy.
		exec.Command("docker", "exec", "-u", "root", containerName, "rm", "-f", "/tmp/monitor-hosts.txt").Run()
	}

	// Re-copy the allowlist into the proxyuser-readable /tmp path, ensure the
	// monitor copy is readable, then SIGHUP so the proxy reloads both.
	cmd := exec.Command("docker", "exec", "-u", "root", containerName, "bash", "-c",
		"cp /home/claude/allowed-domains.txt.enc /tmp/allowed-domains.txt.enc && "+
			"chmod 644 /tmp/allowed-domains.txt.enc && "+
			"chmod 644 /tmp/monitor-hosts.txt 2>/dev/null || true && "+
			"pkill -HUP -x allowlist-proxy")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// detachedSessionName derives the tmux session name for the current project's container.
func detachedSessionName() (session string, container string, err error) {
	cfg, err := readConfig(getProjectDir())
	if err != nil {
		return "", "", fmt.Errorf("no project configured — run sandclaude init first")
	}
	container = containerNameForWorkspace(cfg.Workspace)
	session = tmuxSessionNameForContainer(container)
	return session, container, nil
}

// cmdCapture prints the last 100 lines of the detached session's terminal output.
func cmdCapture() error {
	session, _, err := detachedSessionName()
	if err != nil {
		return err
	}
	cmd := exec.Command("tmux", "capture-pane", "-t", session, "-p", "-S", "-100")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to capture pane from session '%s': %w\n\nIs the session running? Check: tmux list-sessions", session, err)
	}
	return nil
}

// cmdSend sends a prompt to the inner Claude running in the detached session.
func cmdSend(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sandclaude send <prompt>")
	}
	session, _, err := detachedSessionName()
	if err != nil {
		return err
	}
	prompt := strings.Join(args, " ")

	// Send the prompt text and the Enter key as two separate send-keys calls. Claude's
	// TUI treats a trailing Enter in the same call as a literal newline in the input box
	// (the prompt is typed but never submitted); a separate Enter event submits it.
	typeCmd := exec.Command("tmux", "send-keys", "-t", session, "--", prompt)
	typeCmd.Stdout = os.Stdout
	typeCmd.Stderr = os.Stderr
	if err := typeCmd.Run(); err != nil {
		return fmt.Errorf("failed to type prompt into session '%s': %w", session, err)
	}

	// Brief pause so the TUI registers the typed text before the submit key.
	time.Sleep(300 * time.Millisecond)

	enterCmd := exec.Command("tmux", "send-keys", "-t", session, "Enter")
	enterCmd.Stdout = os.Stdout
	enterCmd.Stderr = os.Stderr
	if err := enterCmd.Run(); err != nil {
		return fmt.Errorf("failed to submit prompt to session '%s': %w", session, err)
	}

	log.Printf("Sent to %s: %s", session, prompt)
	return nil
}

// cmdAttach attaches the current terminal to the detached session for interactive viewing.
func cmdAttach() error {
	session, _, err := detachedSessionName()
	if err != nil {
		return err
	}
	cmd := exec.Command("tmux", "attach-session", "-t", session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// usage prints help information
func usage() {
	fmt.Println("sandclaude - Sandboxed Claude Code with network firewall")
	fmt.Println()
	fmt.Println("Usage: sandclaude [--debug] <command> [options]")
	fmt.Println()
	fmt.Println("Global flags:")
	fmt.Println("  --debug                  Enable verbose debug logging (port scanning, volume mounts, docker args, etc.)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init                     Initialize ./.sandclaude/ in the current directory")
	fmt.Println("  update                   Update project config (preserves credentials and allowlist key)")
	fmt.Println("  start [flags]            Start Claude Code detached and open it in the dashboard (browser-first)")
	fmt.Println("    --foreground                   Run attached to this terminal instead (classic interactive mode)")
	fmt.Println("    --disable-firewall             Skip firewall initialization")
	fmt.Println("    --passthrough-firewall-and-write   Keep proxy but allow all domains; write unknown ones to allowed-domains.txt")
	fmt.Println("    --disable-dind                 Skip inner dockerd startup")
	fmt.Println("    --keep-devfiles                Do not hide .devcontainer from the container (skip tmpfs overlay)")
	fmt.Println("  dev [flags]              Start detached in a tmux session for closed-loop development")
	fmt.Println("                           (same flags as start; observe/drive with capture/send/attach)")
	fmt.Println("  list                     Show ./.sandclaude/ configuration")
	fmt.Println("  remove                   Remove ./.sandclaude/ directory after confirmation")
	fmt.Println("  firewall-reload          Encrypt allowed-domains.txt and SIGHUP proxy")
	fmt.Println("  firewall-monitor         Tail allowlist proxy log in running container")
	fmt.Println("  monitor [list|add <host>|remove <host>|clear]   Selective mitm: hosts routed through mitm")
	fmt.Println("    (empty list = monitor all allowed hosts; others allowed+logged but direct-dialed)")
	fmt.Println("  mitm-ports [list|add <port>|remove <port>|reset]   Ports eligible for mitm (default 80,443)")
	fmt.Println("  set-cred <host> <header|url_param> <name> <value>   Add/update an injected credential (live)")
	fmt.Println("  unset-cred <host>        Remove an injected credential")
	fmt.Println("  proxy-apply              Re-apply proxy config to the running proxies (no restart)")
	fmt.Println("  shell                    Open bash shell in container")
	fmt.Println("  populate-proxy-credentials [--project]   Populate credentials from 'claude setup-token'")
	fmt.Println("    (default: global ~/.sandclaude/proxy-credentials.json; --project: this project's override)")
	fmt.Println("  rebuild [--destroy] [--destroy-inner]   Force rebuild container image")
	fmt.Println("    --destroy        Remove existing outer image/container first (full rebuild from scratch, implies --no-cache)")
	fmt.Println("    --destroy-inner  Wipe inner docker data: all inner images and volumes")
	fmt.Println("  capture                  Print inner Claude output (when started without a TTY)")
	fmt.Println("  send <prompt>            Send a prompt to inner Claude in the detached session")
	fmt.Println("  attach                   Attach interactively to the detached session")
	fmt.Println("  dashboard                Start (or print the URL of) the host-wide project dashboard")
	fmt.Println("  dashboard stop           Stop the dashboard server")
	fmt.Println("  help                     Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  sandclaude init                    # Initialize ./.sandclaude/ in the current directory")
	fmt.Println("  sandclaude start                   # Start detached + open in the dashboard (browser-first)")
	fmt.Println("  sandclaude start --foreground      # Classic interactive session in this terminal")
	fmt.Println("  sandclaude dev                     # Start detached in tmux for closed-loop development")
	fmt.Println("  sandclaude start --debug           # Start with verbose debug logging")
	fmt.Println("  sandclaude --debug start           # Same (--debug works in any position)")
	fmt.Println("  sandclaude start --disable-firewall              # Start without firewall")
	fmt.Println("  sandclaude start --passthrough-firewall-and-write   # Allow all, log unknowns to allowed-domains.txt")
	fmt.Println("  sandclaude populate-proxy-credentials            # Set global credentials")
	fmt.Println("  sandclaude populate-proxy-credentials --project  # Set a project-specific override")
	fmt.Println("  sandclaude shell                   # Debug container")
	fmt.Println("  sandclaude capture                 # Read inner Claude output (non-interactive start)")
	fmt.Println("  sandclaude send 'fix the bug'      # Send a prompt to inner Claude")
	fmt.Println("  sandclaude attach                  # Attach interactively to the running session")
	fmt.Println("  sandclaude dashboard                # Start/open the cross-project dashboard")
	fmt.Println("  sandclaude dashboard stop            # Stop the dashboard")
	fmt.Println()
	fmt.Println("Per-project config lives in ./.sandclaude/ (relative to the current directory):")
	fmt.Println("  ./.sandclaude/project/config.json          — workspace, proxy, dind settings")
	fmt.Println("  ./.sandclaude/project/.allowlist-key       — allowlist encryption key (never commit)")
	fmt.Println("  ./.sandclaude/allowed-domains.txt[.enc]    — firewall allowlist")
	fmt.Println("  ./.sandclaude/project/proxy-credentials.json — optional per-project credential override")
	fmt.Println()
	fmt.Println("Global config lives in ~/.sandclaude/ (override with $SANDCLAUDE_HOME):")
	fmt.Println("  ~/.sandclaude/assets/                       — Docker build context + support files (installed)")
	fmt.Println("  ~/.sandclaude/proxy-credentials.json        — shared credentials (project override wins per-domain)")
	fmt.Println()
}

// Main is the CLI entrypoint. It is invoked by cmd/sandclaude/main.go.
func Main() {
	log.SetFlags(0) // Remove timestamp prefix

	// Scan all args for --debug before command dispatch so it works in any position
	// e.g. `sandclaude --debug start` or `sandclaude start --debug`
	filteredArgs := os.Args[:1]
	for _, arg := range os.Args[1:] {
		if arg == "--debug" {
			debugMode = true
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}
	os.Args = filteredArgs

	if debugMode {
		log.Println("[DEBUG] Debug logging enabled")
	}

	// Get command
	command := "help"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	var err error

	switch command {
	case "init":
		err = cmdInit()

	case "update":
		err = cmdUpdate()

	case "start":
		err = cmdStart(os.Args[2:])

	case "dev":
		err = cmdDev(os.Args[2:])

	case "list":
		err = cmdList()

	case "remove":
		err = cmdRemove()

	case "firewall-reload":
		err = cmdFirewallReload()

	case "firewall-monitor":
		err = cmdFirewallMonitor()

	case "monitor":
		err = cmdMonitor(os.Args[2:])

	case "mitm-ports":
		err = cmdMitmPorts(os.Args[2:])

	case "set-cred":
		err = cmdSetCred(os.Args[2:])

	case "unset-cred":
		err = cmdUnsetCred(os.Args[2:])

	case "proxy-apply":
		err = cmdProxyApply()

	case "shell":
		err = cmdShell()

	case "rebuild":
		destroy := false
		destroyInner := false
		for _, arg := range os.Args[2:] {
			switch arg {
			case "--destroy":
				destroy = true
			case "--destroy-inner":
				destroyInner = true
			}
		}
		err = cmdRebuild(destroy, destroyInner)

	case "populate-proxy-credentials":
		projectScope := false
		for _, arg := range os.Args[2:] {
			if arg == "--project" {
				projectScope = true
			}
		}
		err = cmdPopulateProxyCredentials(projectScope)

	case "capture":
		err = cmdCapture()

	case "send":
		err = cmdSend(os.Args[2:])

	case "attach":
		err = cmdAttach()

	case "dashboard":
		err = cmdDashboard(os.Args[2:])

	case "dashboard-serve": // internal only, spawned by `sandclaude dashboard`
		err = cmdDashboardServe(os.Args[2:])

	case "help", "--help", "-h":
		usage()
		return

	default:
		fmt.Printf("Error: Unknown command '%s'\n\n", command)
		usage()
		os.Exit(1)
	}

	if err != nil {
		log.Fatalf("Error: %v", err)
	}
}
