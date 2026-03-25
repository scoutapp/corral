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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProxyPort   = "8080"
	mitmwebProcessName = "mitmweb"
)

type SandClaude struct {
	proxyCmd               *exec.Cmd
	proxyPort              string
	credentialsFile        string
	addonScript            string
	scriptDir              string
	proxyEnabled           bool
	disableFirewall        bool
	disableFirewallAndWrite bool
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

// killExistingMitmweb finds and kills any existing mitmweb processes
func (sc *SandClaude) killExistingMitmweb() error {
	// Use pgrep to find mitmweb processes
	cmd := exec.Command("pgrep", "-f", mitmwebProcessName)
	output, err := cmd.Output()

	if err != nil {
		// Exit code 1 means no processes found, which is fine
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		// Other errors are actual problems
		return fmt.Errorf("failed to check for existing processes: %w", err)
	}

	// Parse PIDs and kill them
	pids := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, pid := range pids {
		if pid == "" {
			continue
		}
		log.Printf("Killing existing mitmweb process (PID: %s)...", pid)
		killCmd := exec.Command("kill", "-9", pid)
		if err := killCmd.Run(); err != nil {
			log.Printf("Warning: failed to kill process %s: %v", pid, err)
		}
	}

	// Give processes time to die
	time.Sleep(500 * time.Millisecond)

	return nil
}

// startProxy starts the mitmweb proxy process
func (sc *SandClaude) startProxy() error {
	log.Println("Starting mitmproxy credential injection proxy...")
	log.Printf("Port: %s", sc.proxyPort)
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
	Workspace    string `json:"workspace"`
	GitHubRepo   string `json:"github_repo,omitempty"`
	AWSEnabled   bool   `json:"aws_enabled,omitempty"`
	ProxyEnabled bool   `json:"proxy_enabled,omitempty"`
	CreatedAt    string `json:"created_at"`
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
	containerName := "sandclaude"
	args := []string{"run", "--rm", "-it", "--name", containerName,
		"--cap-add=NET_ADMIN",
		"--cap-add=NET_RAW",
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

	// Mount empty tmpfs over .devcontainer to hide it from the container
	args = append(args, "--tmpfs", fmt.Sprintf("%s/.devcontainer:rw,noexec,nosuid,size=1m", workspace))

	// Mount the plaintext allowlist to a stable path outside the tmpfs'd .devcontainer dir.
	// (.devcontainer is hidden under tmpfs so any bind-mount targeting a path inside it
	// would have Docker auto-create the target as a directory, not a file.)
	cwd, _ := os.Getwd()
	allowlistPath := filepath.Join(cwd, "allowlist-proxy", "allowed-domains.txt")
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
			"-e", "HTTP_PROXY=http://host.docker.internal:8080",
			"-e", "HTTPS_PROXY=http://host.docker.internal:8080",
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

	if cfg.ProxyEnabled {
		sc.proxyEnabled = true
		log.Println("Proxy configured for this project, starting...")
		log.Println()

		if err := sc.killExistingMitmweb(); err != nil {
			return err
		}
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
    "value": "Bearer ghp_real_token_here"
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

	return nil
}

// cmdStart starts Claude Code
func cmdStart(args []string) error {
	disableFirewall := false
	disableFirewallAndWrite := false

	for _, arg := range args {
		switch arg {
		case "--disable-firewall":
			disableFirewall = true
		case "--disable-firewall-and-write":
			disableFirewallAndWrite = true
		}
	}

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	sc.disableFirewall = disableFirewall
	sc.disableFirewallAndWrite = disableFirewallAndWrite
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

// cmdRebuild rebuilds the Docker image
func cmdRebuild() error {
	sc, err := NewSandClaude()
	if err != nil {
		return err
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

	buildCmd := exec.Command("docker", "build",
		"--build-arg", fmt.Sprintf("USER_ID=%s", userID),
		"--build-arg", fmt.Sprintf("GROUP_ID=%s", groupID),
		"-t", "sandclaude",
		sc.scriptDir,
	)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
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
	fmt.Println("  start [flags]            Start Claude Code (uses ./project/ config)")
	fmt.Println("    --disable-firewall             Skip firewall initialization")
	fmt.Println("    --disable-firewall-and-write   Keep proxy but allow all domains; write unknown ones to allowed-domains.txt")
	fmt.Println("  list                     Show ./project/ configuration")
	fmt.Println("  remove                   Remove ./project/ directory after confirmation")
	fmt.Println("  firewall-reload          Encrypt allowed-domains.txt and SIGHUP proxy")
	fmt.Println("  firewall-monitor         Tail allowlist proxy log in running container")
	fmt.Println("  shell                    Open bash shell in container")
	fmt.Println("  copy <target>            Copy sandclaude files to target directory")
	fmt.Println("  rebuild                  Force rebuild container image")
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
		err = cmdRebuild()

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
