package main

import (
	"bufio"
	"fmt"
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
	defaultProxyPort        = "8080"
	defaultCredentialsPath  = ".config/sandclaude/proxy-credentials.json"
	mitmwebProcessName      = "mitmweb"
)

type SandClaude struct {
	proxyCmd         *exec.Cmd
	proxyPort        string
	credentialsFile  string
	addonScript      string
	scriptDir        string
	proxyEnabled     bool
	disableFirewall  bool
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

	credentialsFile := os.Getenv("SANDCLAUDE_PROXY_CREDS")
	if credentialsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		credentialsFile = filepath.Join(home, defaultCredentialsPath)
	}

	addonScript := filepath.Join(scriptDir, "proxy-addon.py")

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
	logFile, err := os.OpenFile("mitm.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open mitm.log: %w", err)
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
	log.Printf("Logs written to: mitm.log\n")

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

// startDocker starts the Docker container with Claude Code
func (sc *SandClaude) startDocker(projectDir, workspace, project string) error {
	// Build image if needed
	imageName := "sandclaude"
	if err := sc.ensureImage(imageName); err != nil {
		return err
	}

	log.Printf("🚀 Starting sandclaude for project: %s\n", project)
	log.Printf("📁 Workspace: %s\n", workspace)
	log.Println()

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	claudeConfig := filepath.Join(home, ".claude")

	// Check Claude credentials
	if sc.proxyEnabled {
		// Proxy mode: generate dummy auth token
		log.Println("Setting up dummy auth token for proxy mode...")
		// Generate realistic-looking dummy token
		dummyToken := "sk-ant-oat01-" + strings.Repeat("0", 86) + "-" + strings.Repeat("0", 8)
		os.Setenv("DUMMY_AUTH_TOKEN", dummyToken)
		log.Println("✅ Dummy auth token created (proxy will inject real credentials)")
	} else {
		// Normal mode: require real credentials
		credPath := filepath.Join(claudeConfig, ".credentials.json")
		if _, err := os.Stat(credPath); os.IsNotExist(err) {
			return fmt.Errorf("no Claude credentials found at %s\nRun 'claude' locally first to log in via OAuth", credPath)
		}
	}

	// Get gh token if available
	ghToken := ""
	cmd := exec.Command("gh", "auth", "token")
	if output, err := cmd.Output(); err == nil {
		ghToken = strings.TrimSpace(string(output))
	}

	// Build docker args
	containerName := "sandclaude-" + project
	args := []string{"run", "--rm", "-it", "--name", containerName,
		"--cap-add=NET_ADMIN",
		"--cap-add=NET_RAW",
	}

	if sc.disableFirewall {
		args = append(args, "-e", "DISABLE_FIREWALL=1")
	}

	args = append(args,
		"-v", fmt.Sprintf("%s:/home/claude/.claude", claudeConfig),
		"-v", fmt.Sprintf("%s:/home/claude/.claude.json", filepath.Join(home, ".claude.json")),
		"-v", fmt.Sprintf("%s:/home/claude/.config/gh:ro", filepath.Join(home, ".config/gh")),
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
		"-e", fmt.Sprintf("PROJECT_NAME=%s", project),
	)

	if ghToken != "" {
		args = append(args, "-e", fmt.Sprintf("GH_TOKEN=%s", ghToken))
	}

	// Mount GitHub repo configuration
	repoFile := filepath.Join(projectDir, "config/repo")
	if data, err := os.ReadFile(repoFile); err == nil {
		repo := strings.TrimSpace(string(data))
		args = append(args, "-e", fmt.Sprintf("GITHUB_REPO=%s", repo))
	}

	// Mount AWS credentials if enabled
	awsEnabledFile := filepath.Join(projectDir, "config/aws_enabled")
	if _, err := os.Stat(awsEnabledFile); err == nil {
		awsDir := filepath.Join(home, ".aws")
		if _, err := os.Stat(awsDir); err == nil {
			args = append(args,
				"-v", fmt.Sprintf("%s:/home/claude/.aws:ro", awsDir),
				"-e", "AWS_SHARED_CREDENTIALS_FILE=/home/claude/.aws/credentials",
				"-e", "AWS_CONFIG_FILE=/home/claude/.aws/config",
			)
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
			"-e", fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", os.Getenv("DUMMY_AUTH_TOKEN")),
		)

		// Create mitmproxy directory if it doesn't exist
		mitmDir := filepath.Join(home, ".mitmproxy")
		os.MkdirAll(mitmDir, 0755)

		// Mount mitmproxy certificate directory
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/.mitmproxy:ro", mitmDir))

		log.Println("🔒 Proxy mode enabled")
		certPath := filepath.Join(mitmDir, "mitmproxy-ca-cert.pem")
		if _, err := os.Stat(certPath); err == nil {
			log.Println("   Certificate found on host: ✅")
		} else {
			log.Println("   Certificate not found on host: ❌")
			log.Println("   It will be generated when proxy starts")
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
func (sc *SandClaude) Run(project string) error {
	log.Println("============================================================")
	log.Println("SandClaude - Secure Claude Code Environment")
	log.Println("============================================================")
	log.Println()

	// Get project directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".config/sandclaude/projects")
	projectDir := filepath.Join(projectsDir, project)

	// Check if project exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("project '%s' not found\nRun: sandclaude init %s", project, project)
	}

	// Get workspace
	workspaceFile := filepath.Join(projectDir, "config/workspace")
	workspaceBytes, err := os.ReadFile(workspaceFile)
	if err != nil {
		return fmt.Errorf("no workspace configured for project '%s'", project)
	}
	workspace := strings.TrimSpace(string(workspaceBytes))

	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		return fmt.Errorf("workspace not found: %s", workspace)
	}

	// Check if proxy is enabled for this project
	proxyEnabledFile := filepath.Join(projectDir, "config/proxy_enabled")
	if _, err := os.Stat(proxyEnabledFile); err == nil {
		// Proxy is configured, start it automatically
		sc.proxyEnabled = true
		log.Println("🔒 Proxy configured for this project, starting...")
		log.Println()

		// Kill any existing mitmweb processes
		if err := sc.killExistingMitmweb(); err != nil {
			return err
		}

		// Start proxy
		if err := sc.startProxy(); err != nil {
			return err
		}

		// Set up signal handling for cleanup
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		// Cleanup goroutine
		go func() {
			<-sigChan
			log.Println("\nReceived interrupt signal, cleaning up...")
			sc.stopProxy()
			os.Exit(0)
		}()
	}

	// Start Docker container (blocks until it exits)
	err = sc.startDocker(projectDir, workspace, project)

	// Cleanup proxy when Docker exits
	if sc.proxyEnabled {
		sc.stopProxy()
	}

	return err
}

// getProjectName gets project name from argument or current directory
func getProjectName(arg string) string {
	if arg != "" {
		return arg
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return filepath.Base(cwd)
}

// getProjectDir returns the project configuration directory
func getProjectDir(project string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get home directory: %v", err)
	}
	return filepath.Join(home, ".config/sandclaude/projects", project)
}

// cmdInit initializes a new project
func cmdInit(project string) error {
	if project == "" {
		// Use current directory name as default
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		project = filepath.Base(cwd)

		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Project name (default: %s): ", project)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		inputTrimmed := strings.TrimSpace(input)
		if inputTrimmed != "" {
			project = inputTrimmed
		}
		if project == "" {
			return fmt.Errorf("project name required")
		}
	}

	projectDir := getProjectDir(project)

	// Check if project already exists
	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("project '%s' already exists at %s", project, projectDir)
	}

	log.Printf("Initializing project: %s\n", project)
	log.Println()

	// Create project structure
	os.MkdirAll(filepath.Join(projectDir, "credentials"), 0700)
	os.MkdirAll(filepath.Join(projectDir, "config"), 0755)

	// Ask for repository (optional)
	log.Println()
	if askYesNo("Enable GitHub issue monitoring?") {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("GitHub repository (e.g. owner/repo): ")
		repo, _ := reader.ReadString('\n')
		repo = strings.TrimSpace(repo)
		if repo != "" {
			os.WriteFile(filepath.Join(projectDir, "config/repo"), []byte(repo), 0644)
			log.Printf("✅ GitHub monitoring enabled for %s\n", repo)
		} else {
			log.Println("⚠️  No repo provided. GitHub monitoring disabled.")
		}
	} else {
		log.Println("⚠️  GitHub monitoring disabled")
	}

	// Ask for AWS credentials (optional)
	log.Println()
	if askYesNo("Mount AWS credentials?") {
		os.WriteFile(filepath.Join(projectDir, "config/aws_enabled"), []byte("enabled"), 0644)
		log.Println("✅ AWS credentials will be mounted from ~/.aws")
	} else {
		log.Println("⚠️  AWS credentials will not be mounted")
	}

	// Ask for proxy usage (optional)
	log.Println()
	if askYesNo("Enable credential proxy (hides secrets from Claude)?") {
		os.WriteFile(filepath.Join(projectDir, "config/proxy_enabled"), []byte("enabled"), 0644)
		log.Println("✅ Proxy mode enabled - Claude will use dummy credentials")
		log.Println("   Configure real credentials in ~/.config/sandclaude/proxy-credentials.json")
	} else {
		log.Println("⚠️  Proxy mode disabled")
	}

	// Ask for workspace directory (default to parent of current directory)
	log.Println()
	cwd, _ := os.Getwd()
	defaultWorkspace := filepath.Dir(cwd)
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Workspace directory (default: %s): ", defaultWorkspace)
	workspaceInput, _ := reader.ReadString('\n')
	workspace := strings.TrimSpace(workspaceInput)
	if workspace == "" {
		workspace = defaultWorkspace
	}
	os.WriteFile(filepath.Join(projectDir, "config/workspace"), []byte(workspace), 0644)

	// Create workspace if it doesn't exist
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		if askYesNo("Workspace doesn't exist. Create it?") {
			os.MkdirAll(workspace, 0755)
			log.Printf("Created workspace: %s\n", workspace)
		}
	}

	// Save metadata
	metadata := fmt.Sprintf(`{
  "project": "%s",
  "created": "%s",
  "workspace": "%s"
}`, project, time.Now().UTC().Format(time.RFC3339), workspace)
	os.WriteFile(filepath.Join(projectDir, "config/metadata.json"), []byte(metadata), 0644)

	log.Println()
	log.Printf("✅ Project '%s' initialized at %s\n", project, projectDir)
	log.Println()
	log.Printf("Credentials stored securely in: %s/credentials/\n", projectDir)
	log.Printf("Configuration: %s/config/\n", projectDir)
	log.Println()
	log.Println("Next steps:")
	log.Printf("  sandclaude start %s    # Start Claude Code\n", project)
	log.Println("  sandclaude start              # Start (will prompt for project)")
	log.Println()

	return nil
}

// cmdStart starts Claude Code for a project
func cmdStart(args []string) error {
	project := ""
	disableFirewall := false

	for _, arg := range args {
		if arg == "--disable-firewall" {
			disableFirewall = true
		} else if !strings.HasPrefix(arg, "--") {
			project = arg
		}
	}

	if project == "" {
		// Use current directory name as default
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		project = filepath.Base(cwd)

		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Project name (default: %s): ", project)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		inputTrimmed := strings.TrimSpace(input)
		if inputTrimmed != "" {
			project = inputTrimmed
		}
		if project == "" {
			return fmt.Errorf("project name required")
		}
	}

	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	sc.disableFirewall = disableFirewall
	return sc.Run(project)
}

// cmdList lists all configured projects
func cmdList() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".config/sandclaude/projects")

	entries, err := os.ReadDir(projectsDir)
	if err != nil || len(entries) == 0 {
		log.Println("No projects configured")
		log.Println("Run: sandclaude init <project>")
		return nil
	}

	log.Println("Configured projects:")
	log.Println()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		project := entry.Name()
		projectDir := filepath.Join(projectsDir, project)

		log.Printf("  %s\n", project)

		// Show workspace
		if data, err := os.ReadFile(filepath.Join(projectDir, "config/workspace")); err == nil {
			log.Printf("    Workspace: %s\n", strings.TrimSpace(string(data)))
		}

		// Show GitHub repo
		if data, err := os.ReadFile(filepath.Join(projectDir, "config/repo")); err == nil {
			log.Printf("    GitHub: %s\n", strings.TrimSpace(string(data)))
		}

		// Show AWS status
		if _, err := os.Stat(filepath.Join(projectDir, "config/aws_enabled")); err == nil {
			log.Println("    AWS: enabled")
		}

		log.Println()
	}

	return nil
}

// cmdFirewallMonitor tails the allowlist proxy log inside the running container
func cmdFirewallMonitor(project string) error {
	project = getProjectName(project)
	containerName := "sandclaude-" + project

	cmd := exec.Command("docker", "inspect", "--format={{.Config.WorkingDir}}", containerName)
	workdirOut, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("no running container found for project '%s'", project)
	}
	workdir := strings.TrimSpace(string(workdirOut))

	// less +F: tails live; Ctrl+C to stop following and search, F to resume
	dockerCmd := exec.Command("docker", "exec", "-it", containerName,
		"less", "+F", workdir+"/.firewall/proxy.log")
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	return dockerCmd.Run()
}

// cmdShell opens a bash shell in the container
func cmdShell(project string) error {
	project = getProjectName(project)
	projectDir := getProjectDir(project)

	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("project '%s' not found", project)
	}

	// Get workspace
	workspaceBytes, err := os.ReadFile(filepath.Join(projectDir, "config/workspace"))
	if err != nil {
		// Use current directory if no workspace configured
		workspace, _ := os.Getwd()
		workspaceBytes = []byte(workspace)
	}
	workspace := strings.TrimSpace(string(workspaceBytes))

	// Ensure image exists
	sc, err := NewSandClaude()
	if err != nil {
		return err
	}

	imageName := "sandclaude"
	if err := sc.ensureImage(imageName); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	claudeConfig := filepath.Join(home, ".claude")

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
		"-v", fmt.Sprintf("%s:/home/claude/.claude", claudeConfig),
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

// cmdRemove removes a project
func cmdRemove(project string) error {
	if project == "" {
		// Use current directory name as default
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		project = filepath.Base(cwd)

		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Project name to remove (default: %s): ", project)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		inputTrimmed := strings.TrimSpace(input)
		if inputTrimmed != "" {
			project = inputTrimmed
		}
		if project == "" {
			return fmt.Errorf("project name required")
		}
	}

	projectDir := getProjectDir(project)

	// Check if project exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return fmt.Errorf("project '%s' not found", project)
	}

	// Confirm deletion
	log.Printf("⚠️  This will permanently delete project '%s'\n", project)
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

	log.Printf("✅ Project '%s' removed\n", project)
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

	log.Printf("✅ Files copied to %s\n", targetAbs)
	log.Println()
	log.Println("Next steps:")
	log.Printf("  1. Customize %s/Dockerfile if needed\n", targetAbs)
	log.Printf("  2. Add domains to %s/.firewall/allowed-domains.txt\n", targetAbs)
	log.Printf("  3. Open in VS Code: code %s\n", parentDir)
	log.Println("  4. Click 'Reopen in Container'")
	log.Println()

	return nil
}

// usage prints help information
func usage() {
	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".config/sandclaude/projects")

	fmt.Println("sandclaude - Sandboxed Claude Code with network firewall")
	fmt.Println()
	fmt.Println("Usage: sandclaude <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init [project]           Initialize a new project with credentials")
	fmt.Println("  start [project] [flags]  Start Claude Code for a project (default: current directory)")
	fmt.Println("    --disable-firewall       Skip firewall initialization")
	fmt.Println("  list                     List configured projects")
	fmt.Println("  remove <project>         Remove a project and its configuration")
	fmt.Println("  firewall-monitor [project] Tail allowlist proxy log")
	fmt.Println("  shell [project]          Open bash shell in container")
	fmt.Println("  copy <target>            Copy sandclaude files to target directory")
	fmt.Println("  rebuild                  Force rebuild container image")
	fmt.Println("  help                     Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  sandclaude init myapp              # Setup new project with credentials")
	fmt.Println("  sandclaude start myapp             # Start Claude for 'myapp' project")
	fmt.Println("  sandclaude start                   # Start with current directory name as project")
	fmt.Println("  sandclaude copy ~/my-project       # Copy files to integrate into another project")
	fmt.Println("  sandclaude shell myapp             # Debug container for 'myapp'")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Printf("  Projects are stored in: %s\n", projectsDir)
	fmt.Println("  Each project has its own credentials and configuration")
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
		project := ""
		if len(os.Args) > 2 {
			project = os.Args[2]
		}
		err = cmdInit(project)

	case "start":
		err = cmdStart(os.Args[2:])

	case "list":
		err = cmdList()

	case "remove":
		project := ""
		if len(os.Args) > 2 {
			project = os.Args[2]
		}
		err = cmdRemove(project)

	case "firewall-monitor":
		project := ""
		if len(os.Args) > 2 {
			project = os.Args[2]
		}
		err = cmdFirewallMonitor(project)

	case "shell":
		project := ""
		if len(os.Args) > 2 {
			project = os.Args[2]
		}
		err = cmdShell(project)

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
