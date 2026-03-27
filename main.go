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

type SandClaude struct {
	proxyCmd                *exec.Cmd
	proxyPort               string
	credentialsFile         string
	addonScript             string
	scriptDir               string
	proxyEnabled            bool
	disableFirewall         bool
	disableFirewallAndWrite bool
	disableDind             bool
	dindEnabled             bool
	dindPorts               []string
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
	for port := startPort; port < startPort+100; port++ {
		// Check both 0.0.0.0 (what mitmproxy uses for --listen-port) and
		// 127.0.0.1 (what mitmweb uses for --web-port). A port is only free
		// if both succeed.
		addr1 := fmt.Sprintf("0.0.0.0:%d", port)
		ln1, err1 := net.Listen("tcp", addr1)
		if err1 != nil {
			continue
		}
		ln1.Close()

		addr2 := fmt.Sprintf("127.0.0.1:%d", port)
		ln2, err2 := net.Listen("tcp", addr2)
		if err2 != nil {
			continue
		}
		ln2.Close()

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

	log.Println("Starting mitmproxy credential injection proxy...")
	log.Printf("Port: %s", sc.proxyPort)
	log.Printf("Web UI: http://127.0.0.1:%d", webPort)
	log.Printf("Credentials file: %s", sc.credentialsFile)
	log.Println()
	log.Println("Configure credentials in", sc.credentialsFile, "with format:")
	log.Println(`  {"api.example.com": {"header": "X-API-Key", "value": "secret"}}`)
	log.Println()

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

	log.Printf("mitmweb started with PID %d\n", sc.proxyCmd.Process.Pid)
	log.Printf("Logs written to: %s\n", mitmLog)

	// Give proxy time to start
	time.Sleep(2 * time.Second)

	return nil
}

// stopProxy stops the mitmweb proxy process
func (sc *SandClaude) stopProxy() {
	if sc.proxyCmd != nil && sc.proxyCmd.Process != nil {
		log.Printf("Stopping proxy (PID %d)...", sc.proxyCmd.Process.Pid)

		// Try graceful shutdown first
		if err := sc.proxyCmd.Process.Signal(syscall.SIGTERM); err != nil {
			log.Printf("Failed to send SIGTERM: %v", err)
			// Force kill if graceful shutdown fails
			sc.proxyCmd.Process.Kill()
		}

		// Wait for process to exit
		sc.proxyCmd.Wait()
		log.Println("Proxy stopped")
	}
}

// readAWSCredentials reads AWS credentials from ~/.aws/credentials (INI format)
// Returns access key, secret key, session token (may be empty), and region from ~/.aws/config.
func readAWSCredentials() (accessKey, secretKey, sessionToken, region string, err error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		err = fmt.Errorf("failed to get home directory: %w", homeErr)
		return
	}

	credsPath := filepath.Join(home, ".aws", "credentials")
	credsData, readErr := os.ReadFile(credsPath)
	if readErr != nil {
		err = fmt.Errorf("failed to read ~/.aws/credentials: %w", readErr)
		return
	}

	// Simple INI parser: find [default] section and extract key=value pairs
	inDefault := false
	scanner := bufio.NewScanner(strings.NewReader(string(credsData)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inDefault = line == "[default]"
			continue
		}
		if !inDefault {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		switch k {
		case "aws_access_key_id":
			accessKey = v
		case "aws_secret_access_key":
			secretKey = v
		case "aws_session_token":
			sessionToken = v
		}
	}

	if accessKey == "" || secretKey == "" {
		err = fmt.Errorf("aws_access_key_id or aws_secret_access_key not found in ~/.aws/credentials [default] section")
		return
	}

	// Try to get region from ~/.aws/config
	configPath := filepath.Join(home, ".aws", "config")
	if configData, configErr := os.ReadFile(configPath); configErr == nil {
		inDefaultProfile := false
		configScanner := bufio.NewScanner(strings.NewReader(string(configData)))
		for configScanner.Scan() {
			line := strings.TrimSpace(configScanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "[") {
				inDefaultProfile = line == "[default]" || line == "[profile default]"
				continue
			}
			if !inDefaultProfile {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if k == "region" {
				region = v
				break
			}
		}
	}

	return
}

// ----------------------------------------------------------------------------
// Project config (project/config.json)
// ----------------------------------------------------------------------------

type ProjectConfig struct {
	Workspace    string   `json:"workspace"`
	GitHubRepo   string   `json:"github_repo,omitempty"`
	AWSEnabled   bool     `json:"aws_enabled,omitempty"`
	ProxyEnabled bool     `json:"proxy_enabled,omitempty"`
	DindEnabled  bool     `json:"dind_enabled,omitempty"`
	DindPorts    []string `json:"dind_ports,omitempty"`
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
func (sc *SandClaude) startDocker(cfg *ProjectConfig) error {
	workspace := cfg.Workspace
	// Build image if needed
	imageName := "sandclaude"
	if err := sc.ensureImage(imageName); err != nil {
		return err
	}

	log.Printf("Starting sandclaude\n")
	log.Printf("Workspace: %s\n", workspace)
	log.Println()

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
	} else if sc.disableFirewallAndWrite {
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
		// Proxy mode: generate dummy auth token
		log.Println("Setting up dummy auth token for proxy mode...")
		dummyToken := "sk-ant-oat01-" + strings.Repeat("0", 86) + "-" + strings.Repeat("0", 8)
		args = append(args, "-e", fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", dummyToken))
		log.Println("Dummy auth token created (proxy will inject real credentials)")
	}
	// In non-proxy mode, Claude Code will handle its own authentication

	// Get gh token if available
	ghToken := ""
	cmd := exec.Command("gh", "auth", "token")
	if output, err := cmd.Output(); err == nil {
		ghToken = strings.TrimSpace(string(output))
	}
	if ghToken != "" {
		args = append(args, "-e", fmt.Sprintf("GH_TOKEN=%s", ghToken))
	}

	// Mount .claude directory from host for Claude Code state
	claudeConfig := filepath.Join(home, ".claude")
	args = append(args,
		"-v", fmt.Sprintf("%s:/home/claude/.claude", claudeConfig),
		// Very important that we mount this in. This lives at the user's home directory, at least on Mac x86.
		"-v", fmt.Sprintf("%s:/home/claude/.claude.json", filepath.Join(home, ".claude.json")),
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
	)

	// Shadow the host's ~/.claude/skills with a tmpfs so the container can't write skills
	// back to the host. Then layer skills from three sources on top (all read-only):
	//   1. Host ~/.claude/skills/*  — user's personal skills
	//   2. Repo .claude/skills/*   — skills shipped with sandclaude
	//   3. Workspace .sandclaude/skills/* — project-specific skills
	// Mounting each skill directory individually lets all three coexist without
	// any of them being writable back to the host.
	args = append(args, "--tmpfs", "/home/claude/.claude/skills:rw,noexec,nosuid,size=64m")

	mountSkillDirs := func(srcDir string) {
		if entries, err := os.ReadDir(srcDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					src := filepath.Join(srcDir, entry.Name())
					dst := fmt.Sprintf("/home/claude/.claude/skills/%s", entry.Name())
					args = append(args, "-v", fmt.Sprintf("%s:%s:ro", src, dst))
				}
			}
		}
	}

	// Layer 1: host personal skills
	mountSkillDirs(filepath.Join(home, ".claude", "skills"))
	// Layer 2: repo skills (in .claude/skills/ relative to the binary)
	mountSkillDirs(filepath.Join(sc.scriptDir, ".claude", "skills"))
	// Layer 3: workspace project skills
	mountSkillDirs(filepath.Join(workspace, ".sandclaude", "skills"))

	// If the workspace has a .claude directory, shadow each of its subdirectories with
	// tmpfs (so nothing writes back to the host), then mount the workspace .claude subdir
	// on top read-only. For skills/ we do per-skill-dir mounts so repo and .sandclaude
	// skills layer on top; for all other subdirs (rules, agents, commands, etc.) we mount
	// the whole subdir directory.
	workspaceClaudeDir := filepath.Join(workspace, ".claude")
	if topEntries, err := os.ReadDir(workspaceClaudeDir); err == nil {
		for _, topEntry := range topEntries {
			if !topEntry.IsDir() {
				continue
			}
			subName := topEntry.Name()
			containerSubPath := fmt.Sprintf("/home/claude/.claude/%s", subName)
			// Shadow with tmpfs so container writes stay in memory, not on the host.
			args = append(args, "--tmpfs", fmt.Sprintf("%s:rw,noexec,nosuid,size=64m", containerSubPath))

			hostSubPath := filepath.Join(workspaceClaudeDir, subName)
			if subName == "skills" {
				// For skills, mount each skill subdirectory individually so that
				// repo skills and .sandclaude skills mounted above can coexist.
				if skillEntries, err := os.ReadDir(hostSubPath); err == nil {
					for _, skillEntry := range skillEntries {
						if skillEntry.IsDir() {
							src := filepath.Join(hostSubPath, skillEntry.Name())
							dst := fmt.Sprintf("%s/%s", containerSubPath, skillEntry.Name())
							args = append(args, "-v", fmt.Sprintf("%s:%s:ro", src, dst))
						}
					}
				}
			} else {
				// For rules, agents, commands, etc., mount the whole subdirectory.
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", hostSubPath, containerSubPath))
			}
		}
	}

	// Mount empty tmpfs over .devcontainer to hide it from the container
	args = append(args, "--tmpfs", fmt.Sprintf("%s/.devcontainer:rw,noexec,nosuid,size=1m", workspace))

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
	if sc.disableFirewallAndWrite {
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

	// GitHub repo from config
	if cfg.GitHubRepo != "" {
		args = append(args, "-e", fmt.Sprintf("GITHUB_REPO=%s", cfg.GitHubRepo))
	}

	// AWS credentials if enabled
	if cfg.AWSEnabled {
		awsDir := filepath.Join(home, ".aws")
		if _, err := os.Stat(awsDir); err == nil {
			accessKey, secretKey, sessionToken, region, awsErr := readAWSCredentials()
			if awsErr != nil {
				log.Printf("Warning: AWS credentials requested but could not be read: %v", awsErr)
			} else {
				args = append(args,
					"-e", fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", accessKey),
					"-e", fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", secretKey),
				)
				if sessionToken != "" {
					args = append(args, "-e", fmt.Sprintf("AWS_SESSION_TOKEN=%s", sessionToken))
				}
				if region != "" {
					args = append(args, "-e", fmt.Sprintf("AWS_REGION=%s", region))
				}
			}
		} else {
			log.Println("Warning: AWS credentials requested but ~/.aws not found")
		}
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
			log.Println("Proxy mode enabled — certificate found on host")
		} else {
			log.Println("Proxy mode enabled — certificate not found on host, will be generated when proxy starts")
		}
		log.Println()
	}

	// DinD: signal entrypoint to start inner dockerd and expose ports
	if sc.dindEnabled {
		args = append(args, "-e", "DIND_ENABLED=1")
		for _, port := range sc.dindPorts {
			args = append(args, "-p", port)
		}
		log.Printf("🐳 DinD enabled")
		if len(sc.dindPorts) > 0 {
			log.Printf("   Ports: %s", strings.Join(sc.dindPorts, ", "))
		}
		log.Println()
	}

	args = append(args, imageName)

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
		return nil // Image exists
	}

	// Build image
	log.Printf("Building %s image...\n", imageName)

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
func (sc *SandClaude) Run() error {
	log.Println("============================================================")
	log.Println("SandClaude - Secure Claude Code Environment")
	log.Println("============================================================")
	log.Println()

	projectDir := getProjectDir()
	cfg, err := readConfig(projectDir)
	if err != nil {
		return err
	}

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
		log.Println("Proxy configured for this project, starting...")
		log.Println()

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

	err = sc.startDocker(cfg)

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

	// GitHub monitoring
	reader := bufio.NewReader(os.Stdin)
	if cfg.GitHubRepo != "" {
		fmt.Printf("GitHub repository (current: %s, blank to clear): ", cfg.GitHubRepo)
	} else {
		fmt.Print("GitHub repository (e.g. owner/repo, blank to disable): ")
	}
	repoInput, _ := reader.ReadString('\n')
	repoInput = strings.TrimSpace(repoInput)
	if repoInput == "" && cfg.GitHubRepo != "" {
		log.Printf("  GitHub repo unchanged: %s\n", cfg.GitHubRepo)
	} else {
		cfg.GitHubRepo = repoInput
		if repoInput != "" {
			log.Printf("  GitHub repo set to: %s\n", repoInput)
		} else {
			log.Println("  GitHub monitoring disabled")
		}
	}

	log.Println()

	// AWS
	awsPrompt := "n"
	if cfg.AWSEnabled {
		awsPrompt = "Y"
	}
	fmt.Printf("Pass AWS credentials from host ~/.aws? (current: %s) [y/N]: ", awsPrompt)
	awsInput, _ := reader.ReadString('\n')
	awsInput = strings.TrimSpace(strings.ToLower(awsInput))
	if awsInput == "" {
		log.Printf("  AWS unchanged: %v\n", cfg.AWSEnabled)
	} else {
		cfg.AWSEnabled = awsInput == "y" || awsInput == "yes"
		log.Printf("  AWS enabled: %v\n", cfg.AWSEnabled)
	}

	log.Println()

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
		return fmt.Errorf("project already initialized at %s", projectDir)
	}

	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return fmt.Errorf("failed to create project dir: %w", err)
	}

	log.Printf("Initializing project at: %s\n", projectDir)
	log.Println()

	cfg := &ProjectConfig{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// GitHub monitoring
	if askYesNo("Enable GitHub issue monitoring?") {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("GitHub repository (e.g. owner/repo): ")
		repo, _ := reader.ReadString('\n')
		repo = strings.TrimSpace(repo)
		if repo != "" {
			cfg.GitHubRepo = repo
			log.Printf("GitHub monitoring enabled for %s\n", repo)
		} else {
			log.Println("No repo provided. GitHub monitoring disabled.")
		}
	}

	log.Println()

	// AWS credentials
	if askYesNo("Pass AWS credentials from host ~/.aws?") {
		cfg.AWSEnabled = true
		log.Println("AWS credentials will be read from host ~/.aws/credentials")
	}

	log.Println()

	// Docker-in-Docker
	if askYesNo("Enable Docker-in-Docker (inner containers, Claude-accessible)?") {
		cfg.DindEnabled = true
		log.Println("Docker-in-Docker enabled — Claude can start inner containers")
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

	// Credential proxy
	if askYesNo("Enable credential proxy (hides secrets from Claude)?") {
		cfg.ProxyEnabled = true
		log.Println("✅ Proxy mode enabled")
		log.Println()
		log.Println("⚠️  IMPORTANT: You must configure real credentials before starting!")
		log.Println("   A template will be created at: project/proxy-credentials.json")
		log.Println("   Edit this file with your actual API keys/tokens")
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

	// Create dummy proxy credentials template if proxy is enabled
	if cfg.ProxyEnabled {
		proxyCredsPath := filepath.Join(projectDir, "proxy-credentials.json")
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
	disableFirewallAndWrite := false
	disableDind := false

	for _, arg := range args {
		switch arg {
		case "--disable-firewall":
			disableFirewall = true
		case "--disable-firewall-and-write":
			disableFirewallAndWrite = true
		case "--disable-dind":
			disableDind = true
		}
	}

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	sc.disableFirewall = disableFirewall
	sc.disableFirewallAndWrite = disableFirewallAndWrite
	sc.disableDind = disableDind
	return sc.Run()
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
	if cfg.GitHubRepo != "" {
		log.Printf("  GitHub:    %s\n", cfg.GitHubRepo)
	}
	if cfg.AWSEnabled {
		log.Println("  AWS:       enabled")
	}
	if cfg.DindEnabled {
		log.Print("  DinD:      enabled")
		if len(cfg.DindPorts) > 0 {
			log.Printf(" (ports: %s)", strings.Join(cfg.DindPorts, ", "))
		}
		log.Println()
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

	imageName := "sandclaude"
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

// cmdRebuild rebuilds the Docker image, optionally destroying the existing image and container first
func cmdRebuild(destroy bool) error {
	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	if destroy {
		log.Println("Destroying existing sandclaude container and image...")

		// Stop and remove any running container
		stopCmd := exec.Command("docker", "rm", "-f", "sandclaude")
		stopCmd.Stdout = os.Stdout
		stopCmd.Stderr = os.Stderr
		stopCmd.Run() // ignore error — container may not exist

		// Remove the image
		rmiCmd := exec.Command("docker", "rmi", "-f", "sandclaude")
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
	buildArgs = append(buildArgs, "-t", "sandclaude", sc.scriptDir)
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
}

// cmdPopulateProxyCredentials populates proxy-credentials.json interactively using claude setup-token and gh auth token
func cmdPopulateProxyCredentials() error {
	projectDir := getProjectDir()
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("no project found — run: sandclaude init")
	}

	// Read existing credentials file if present, otherwise start fresh
	credsPath := filepath.Join(projectDir, "proxy-credentials.json")
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
	} else if askYesNo("Populate Claude credentials (api.anthropic.com, platform.claude.com, mcp-proxy.anthropic.com)?") {
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

	// GitHub credentials
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Println("'gh' not found in PATH — skipping GitHub credentials")
	} else if askYesNo("Populate GitHub credentials (api.github.com)?") {
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
	fmt.Println("Usage: sandclaude <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init                     Initialize ./project/ structure in current repo")
	fmt.Println("  update                   Update project config (preserves credentials and allowlist key)")
	fmt.Println("  start [flags]            Start Claude Code (uses ./project/ config)")
	fmt.Println("    --disable-firewall             Skip firewall initialization")
	fmt.Println("    --disable-firewall-and-write   Keep proxy but allow all domains; write unknown ones to allowed-domains.txt")
	fmt.Println("    --disable-dind                 Skip inner dockerd startup")
	fmt.Println("  list                     Show ./project/ configuration")
	fmt.Println("  remove                   Remove ./project/ directory after confirmation")
	fmt.Println("  firewall-reload          Encrypt allowed-domains.txt and SIGHUP proxy")
	fmt.Println("  firewall-monitor         Tail allowlist proxy log in running container")
	fmt.Println("  shell                    Open bash shell in container")
	fmt.Println("  populate-proxy-credentials       Interactively populate proxy-credentials.json from 'claude setup-token' and 'gh auth token'")
	fmt.Println("  copy <target>            Copy sandclaude files to target directory")
	fmt.Println("  rebuild [--destroy]      Force rebuild container image (--destroy removes existing image/container first)")
	fmt.Println("  help                     Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  sandclaude init                    # Initialize ./project/ in this repo")
	fmt.Println("  sandclaude start                   # Start Claude Code")
	fmt.Println("  sandclaude start --disable-firewall              # Start without firewall")
	fmt.Println("  sandclaude start --disable-firewall-and-write   # Allow all, log unknowns to allowed-domains.txt")
	fmt.Println("  sandclaude copy ~/my-project       # Copy files to integrate into another project")
	fmt.Println("  sandclaude shell                   # Debug container")
	fmt.Println()
	fmt.Println("Project config lives in ./project/ (relative to current working directory)")
	fmt.Println("  ./project/config.json          — workspace, GitHub, AWS, proxy settings")
	fmt.Println("  ./project/proxy-credentials.json — mitmproxy credential injection")
	fmt.Println()
}

func main() {
	log.SetFlags(0) // Remove timestamp prefix

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
		destroy := len(os.Args) > 2 && os.Args[2] == "--destroy"
		err = cmdRebuild(destroy)

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
