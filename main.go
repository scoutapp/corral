package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	scriptDir                   string
	proxyEnabled                bool
	disableFirewall             bool
	passthroughFirewallAndWrite bool
	disableDind                 bool
	dindEnabled                 bool
	dindPorts                   []string
}

func NewSandClaude() (*SandClaude, error) {
	// Get script directory (where this binary is)
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}
	scriptDir := filepath.Dir(executable)

	// Get configuration from environment
	proxyPort := os.Getenv("SANDCLAUDE_PROXY_PORT")
	if proxyPort == "" {
		proxyPort = defaultProxyPort
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	credentialsFile := os.Getenv("SANDCLAUDE_PROXY_CREDS")
	if credentialsFile == "" {
		credentialsFile = filepath.Join(cwd, "project", "proxy-credentials.json")
	}

	addonScript := filepath.Join(cwd, "proxy-addon.py")

	return &SandClaude{
		proxyPort:       proxyPort,
		credentialsFile: credentialsFile,
		addonScript:     addonScript,
		scriptDir:       scriptDir,
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

// startProxy starts the mitmweb proxy process
func (sc *SandClaude) startProxy() error {
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

	log.Printf("Starting proxy on port %s (web UI: http://127.0.0.1:%d)", sc.proxyPort, webPort)
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

	// Start mitmweb
	sc.proxyCmd = exec.Command(
		"mitmweb",
		"--listen-port", sc.proxyPort,
		"--web-port", fmt.Sprintf("%d", webPort),
		"--set", fmt.Sprintf("credentials_file=%s", sc.credentialsFile),
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
	CreatedAt    string   `json:"created_at"`
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

// getProjectDir returns ./project/ relative to the current working directory.
func getProjectDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}
	return filepath.Join(cwd, "project")
}

// getLogsDir returns ./logs/ relative to the binary's directory.
func getLogsDir() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	binDir := filepath.Dir(exePath)
	return filepath.Join(binDir, "logs")
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
	containerName := "sandclaude_" + filepath.Base(workspace)
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

		cwd, _ := os.Getwd()
		encPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt.enc")
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
		cwd, _ := os.Getwd()
		encPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt.enc")
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

	// Helper function to mount individual items from a .claude subdirectory (read-only)
	mountClaudeSubdirItems := func(sourceLabel, sourceClaudeDir, subName string) {
		srcSubDir := filepath.Join(sourceClaudeDir, subName)
		if entries, err := os.ReadDir(srcSubDir); err == nil {
			debugf("Mounting %s .claude/%s/* (%d items, read-only)", sourceLabel, subName, len(entries))
			for _, entry := range entries {
				entrySrc := filepath.Join(srcSubDir, entry.Name())
				entryDst := fmt.Sprintf("/home/claude/.claude/%s/%s", subName, entry.Name())
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

	repoClaudeDir := filepath.Join(sc.scriptDir, ".claude")
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

	// Determine the allowlist path.
	// When running as the self repo (scriptDir is named "claude_sandbox" or "sandclaude"),
	// write to the canonical allowlist-proxy/allowed-domains.txt inside this repo.
	// Otherwise (running as .devcontainer inside a parent project), write to
	// workspace/.sandclaude/allowed-domains.txt so the project owns its own allowlist.
	cwd, _ := os.Getwd()
	scriptDirName := filepath.Base(sc.scriptDir)
	isSelfRepo := scriptDirName == "claude_sandbox" || scriptDirName == "sandclaude"
	var allowlistPath string
	if isSelfRepo {
		allowlistPath = filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt")
	} else {
		allowlistPath = filepath.Join(workspace, ".sandclaude", "allowed-domains.txt")
	}
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

	logsDir := filepath.Join(cwd, "logs")
	os.MkdirAll(logsDir, 0755)
	args = append(args, "-v", fmt.Sprintf("%s:/home/claude/logs", logsDir))

	// Mount bin/ from the repo so scripts can be edited without rebuilding
	binDir := filepath.Join(sc.scriptDir, "bin")
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

	// Start docker
	dockerCmd := exec.Command("docker", args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	return dockerCmd.Run()
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

	buildCmd := exec.Command("docker", "build",
		"--build-arg", fmt.Sprintf("USER_ID=%s", userID),
		"--build-arg", fmt.Sprintf("GROUP_ID=%s", groupID),
		"-t", imageName,
		sc.scriptDir,
	)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
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

	// Check if DinD is enabled (unless --disable-dind was passed)
	if !sc.disableDind && cfg.DindEnabled {
		sc.dindEnabled = true
		sc.dindPorts = cfg.DindPorts
	}

	if cfg.ProxyEnabled {
		sc.proxyEnabled = true
		log.Println("Proxy enabled, starting...")

		if err := sc.startProxy(); err != nil {
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
		sc.stopProxy()
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
		log.Println("   Run: sandclaude populate-proxy-credentials")
		log.Println("   Otherwise update project/proxy-credentials.json manually")
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

	// Workspace directory
	cwd, _ := os.Getwd()
	defaultWorkspace := filepath.Dir(cwd)
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

	// Create dummy proxy credentials template if proxy is enabled (skip if already populated)
	if cfg.ProxyEnabled {
		proxyCredsPath := filepath.Join(projectDir, "proxy-credentials.json")
		if _, err := os.Stat(proxyCredsPath); err == nil {
			log.Printf("✅ Proxy credentials file already exists at: %s (not overwriting)\n", proxyCredsPath)
		} else {
			proxyCredsTemplate := `{
  "api.anthropic.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
  "platform.claude.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
  "mcp-proxy.anthropic.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
  "api.github.com": {
    "header": "Authorization",
    "value": "token gho_real_token_here"
  }
}
`
			if err := os.WriteFile(proxyCredsPath, []byte(proxyCredsTemplate), 0600); err != nil {
				return fmt.Errorf("failed to write proxy credentials template: %w", err)
			}
			log.Printf("✅ Proxy credentials template created at: %s\n", proxyCredsPath)
			log.Println("   Edit this file with your real credentials")
		}
	}

	// Generate encrypted allowlist using the new key
	log.Println()
	log.Println("Encrypting allowlist...")
	plaintextPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt")
	encPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt.enc")

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

	// If the binary lives inside a .devcontainer directory, we're embedded in a
	// parent project — ensure .devcontainer is listed in that project's .gitignore
	executable, _ := os.Executable()
	scriptDir := filepath.Dir(executable)
	if filepath.Base(scriptDir) == ".devcontainer" {
		parentDir := filepath.Dir(scriptDir)
		gitignorePath := filepath.Join(parentDir, ".gitignore")
		const devcontainerEntry = ".devcontainer"

		existing, err := os.ReadFile(gitignorePath)
		if os.IsNotExist(err) {
			// Create new .gitignore with the entry
			if writeErr := os.WriteFile(gitignorePath, []byte(devcontainerEntry+"\n"), 0644); writeErr == nil {
				log.Printf("✅ Created .gitignore with %s\n", devcontainerEntry)
			}
		} else if err == nil {
			// Check if entry already present
			lines := strings.Split(string(existing), "\n")
			found := false
			for _, line := range lines {
				if strings.TrimSpace(line) == devcontainerEntry {
					found = true
					break
				}
			}
			if !found {
				// Append entry, ensuring we start on a new line
				content := string(existing)
				if len(content) > 0 && !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += devcontainerEntry + "\n"
				if writeErr := os.WriteFile(gitignorePath, []byte(content), 0644); writeErr == nil {
					log.Printf("✅ Added %s to .gitignore\n", devcontainerEntry)
				}
			}
		}
	}

	return nil
}

// cmdStart starts Claude Code
func cmdStart(args []string) error {
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
		case "--debug":
			// already handled globally in main(), ignore here
		}
	}

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	sc.disableFirewall = disableFirewall
	sc.passthroughFirewallAndWrite = passthroughFirewallAndWrite
	sc.disableDind = disableDind
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
	containerName := "sandclaude"

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
	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

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
	buildArgs = append(buildArgs, "-t", "sandclaude-stable", sc.scriptDir)
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
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

// cmdPopulateProxyCredentials populates proxy-credentials.json interactively using claude setup-token
func cmdPopulateProxyCredentials() error {
	projectDir := getProjectDir()
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("no project found — run: sandclaude init")
	}

	credsPath := filepath.Join(projectDir, "proxy-credentials.json")

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

// cmdRemove removes the ./project/ directory
func cmdRemove() error {
	projectDir := getProjectDir()

	// Check if project exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("no project found at %s", projectDir)
	}

	// Confirm deletion
	log.Printf("Warning: This will permanently delete the project directory\n")
	log.Printf("   Location: %s\n", projectDir)
	log.Println()

	if !askYesNo("Are you sure you want to remove this project?") {
		log.Println("Cancelled")
		return nil
	}

	// Remove project directory
	if err := os.RemoveAll(projectDir); err != nil {
		return fmt.Errorf("failed to remove project: %w", err)
	}

	// Remove encrypted allowlist file
	cwd, _ := os.Getwd()
	encPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt.enc")
	if _, err := os.Stat(encPath); err == nil {
		if err := os.Remove(encPath); err != nil {
			log.Printf("Warning: failed to remove encrypted allowlist: %v\n", err)
		} else {
			log.Printf("Removed encrypted allowlist: %s\n", encPath)
		}
	}

	log.Printf("✅ Project removed: %s\n", projectDir)
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

// cmdFirewallReload encrypts allowed-domains.txt → allowed-domains.txt.enc
// using the key from project/.allowlist-key, and sends SIGHUP to the proxy.
func cmdFirewallReload() error {
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

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	plaintextPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt")
	encPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt.enc")

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

	// If a container is running, SIGHUP the proxy so it re-reads the .enc file.
	// The file is bind-mounted, so the container already sees the new content.
	containerName := "sandclaude"
	checkCmd := exec.Command("docker", "inspect", "--format={{.State.Running}}", containerName)
	if out, err := checkCmd.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		sighupCmd := exec.Command("docker", "exec", containerName,
			"bash", "-c", "pkill -HUP -x allowlist-proxy")
		if err := sighupCmd.Run(); err != nil {
			log.Printf("Warning: SIGHUP failed (proxy may not be running yet): %v", err)
		} else {
			log.Printf("✅ SIGHUP sent to allowlist-proxy in container '%s'", containerName)
		}
	} else {
		log.Printf("✅ No running container found — encrypted file ready for next start")
	}

	return nil
}

// cmdCopy copies sandclaude files to a target directory
func cmdCopy(target string) error {
	if target == "" {
		return fmt.Errorf("target directory required\nUsage: sandclaude copy <target>")
	}

	// Remove trailing slash
	target = strings.TrimRight(target, "/")

	// If target doesn't end with .devcontainer, append it
	if !strings.HasSuffix(target, ".devcontainer") {
		target = filepath.Join(target, ".devcontainer")
	}

	// Create target directory
	if err := os.MkdirAll(target, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}

	log.Printf("Copying sandclaude files to: %s\n", targetAbs)
	log.Println()

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	// Files to copy
	files := []string{
		"Dockerfile",
		"entrypoint.sh",
		"launcher.py",
		"proxy-addon.py",
	}

	for _, file := range files {
		src := filepath.Join(sc.scriptDir, file)
		dst := filepath.Join(targetAbs, file)
		if data, err := os.ReadFile(src); err == nil {
			os.WriteFile(dst, data, 0755)
		}
	}

	// Copy devcontainer.json template
	src := filepath.Join(sc.scriptDir, "devcontainer.template.json")
	dst := filepath.Join(targetAbs, "devcontainer.json")
	if data, err := os.ReadFile(src); err == nil {
		os.WriteFile(dst, data, 0644)
	}

	// Copy skill directory
	skillDir := filepath.Join(targetAbs, "skill")
	os.MkdirAll(skillDir, 0755)
	skillSrc := filepath.Join(sc.scriptDir, "skill/SKILL.md")
	skillDst := filepath.Join(skillDir, "SKILL.md")
	if data, err := os.ReadFile(skillSrc); err == nil {
		os.WriteFile(skillDst, data, 0644)
	}

	// Copy gitignore to parent directory if it doesn't exist
	parentDir := filepath.Dir(targetAbs)
	gitignorePath := filepath.Join(parentDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		gitignoreSrc := filepath.Join(sc.scriptDir, ".gitignore")
		if data, err := os.ReadFile(gitignoreSrc); err == nil {
			os.WriteFile(gitignorePath, data, 0644)
		}
	}

	log.Printf("Files copied to %s\n", targetAbs)
	log.Println()
	log.Println("Next steps:")
	log.Printf("  1. Customize %s/Dockerfile if needed\n", targetAbs)
	log.Printf("  2. Edit %s/allowlist-proxy/allowed-domains.txt\n", targetAbs)
	log.Printf("  3. Run: export ALLOWLIST_KEY=<passphrase> && sandclaude firewall-reload\n")
	log.Printf("  4. Open in VS Code: code %s\n", parentDir)
	log.Println("  5. Click 'Reopen in Container'")
	log.Println()

	return nil
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
	fmt.Println("  init                     Initialize ./project/ structure in current repo")
	fmt.Println("  update                   Update project config (preserves credentials and allowlist key)")
	fmt.Println("  start [flags]            Start Claude Code (uses ./project/ config)")
	fmt.Println("    --disable-firewall             Skip firewall initialization")
	fmt.Println("    --passthrough-firewall-and-write   Keep proxy but allow all domains; write unknown ones to allowed-domains.txt")
	fmt.Println("    --disable-dind                 Skip inner dockerd startup")
	fmt.Println("    --keep-devfiles                Do not hide .devcontainer from the container (skip tmpfs overlay)")
	fmt.Println("  list                     Show ./project/ configuration")
	fmt.Println("  remove                   Remove ./project/ directory after confirmation")
	fmt.Println("  firewall-reload          Encrypt allowed-domains.txt and SIGHUP proxy")
	fmt.Println("  firewall-monitor         Tail allowlist proxy log in running container")
	fmt.Println("  shell                    Open bash shell in container")
	fmt.Println("  populate-proxy-credentials       Interactively populate proxy-credentials.json from 'claude setup-token'")
	fmt.Println("  copy <target>            Copy sandclaude files to target directory")
	fmt.Println("  rebuild [--destroy] [--destroy-inner]   Force rebuild container image")
	fmt.Println("    --destroy        Remove existing outer image/container first (full rebuild from scratch, implies --no-cache)")
	fmt.Println("    --destroy-inner  Wipe inner docker data (project/dind-data/): all inner images and volumes")
	fmt.Println("  help                     Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  sandclaude init                    # Initialize ./project/ in this repo")
	fmt.Println("  sandclaude start                   # Start Claude Code")
	fmt.Println("  sandclaude start --debug           # Start with verbose debug logging")
	fmt.Println("  sandclaude --debug start           # Same (--debug works in any position)")
	fmt.Println("  sandclaude start --disable-firewall              # Start without firewall")
	fmt.Println("  sandclaude start --passthrough-firewall-and-write   # Allow all, log unknowns to allowed-domains.txt")
	fmt.Println("  sandclaude copy ~/my-project       # Copy files to integrate into another project")
	fmt.Println("  sandclaude shell                   # Debug container")
	fmt.Println()
	fmt.Println("Project config lives in ./project/ (relative to current working directory)")
	fmt.Println("  ./project/config.json          — workspace, proxy, dind settings")
	fmt.Println("  ./project/proxy-credentials.json — mitmproxy credential injection")
	fmt.Println()
}

func main() {
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

	case "list":
		err = cmdList()

	case "remove":
		err = cmdRemove()

	case "firewall-reload":
		err = cmdFirewallReload()

	case "firewall-monitor":
		err = cmdFirewallMonitor()

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
		err = cmdPopulateProxyCredentials()

	case "copy":
		target := ""
		if len(os.Args) > 2 {
			target = os.Args[2]
		}
		err = cmdCopy(target)

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
