package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/container"
	"github.com/jackrothrock/sandclaude/internal/creds"
	"github.com/jackrothrock/sandclaude/internal/dashboard"
	"github.com/jackrothrock/sandclaude/internal/proxy"
	"github.com/jackrothrock/sandclaude/internal/session"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cmdUpdate updates project config fields without touching credentials or the allowlist key.
func cmdUpdate() error {
	projectDir := config.GetProjectDir()
	cfg, err := config.ReadConfig(projectDir)
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
			if config.AskYesNo("Workspace doesn't exist. Create it?") {
				os.MkdirAll(wsInput, 0755)
				log.Printf("  Created workspace: %s\n", wsInput)
			}
		}
	}

	// Inherit cross-project defaults (monitor-list, mitm-ports) set in the global
	// settings, so a new project starts with the selective-mitm policy you've
	// chosen once. Existing projects are never touched by global defaults.
	if def := dashboard.ReadGlobalDefaults(); len(def.MonitorHosts) > 0 || len(def.MitmPorts) > 0 {
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

	if err := config.WriteConfig(projectDir, cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	log.Println()
	log.Println("✅ Config updated (credentials and allowlist key unchanged)")
	return nil
}

// cmdInit initializes the ./project/ structure
func cmdInit() error {
	projectDir := config.GetProjectDir()

	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("project already initialized at %s\n   To update config run: sandclaude update\n   To remove it run:     sandclaude remove", projectDir)
	}

	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return fmt.Errorf("failed to create project dir: %w", err)
	}

	log.Printf("Initializing project at: %s\n", projectDir)
	log.Println()

	cfg := &config.ProjectConfig{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	log.Println()

	// Credential proxy
	if config.AskYesNo("RECOMMENDED: Enable credential proxy (hides secrets from Claude)?") {
		cfg.ProxyEnabled = true
		log.Println("✅ Proxy mode enabled")
		log.Println()
		log.Println("⚠️  IMPORTANT: You must configure real credentials before starting!")
		log.Printf("   Global credentials live at: %s\n", creds.GlobalCredentialsPath())
		log.Println("   Run: sandclaude populate-proxy-credentials")
		log.Println("   For a project-specific override, run: sandclaude populate-proxy-credentials --project")
	}

	log.Println()

	// tmux
	if config.AskYesNo("Launch with tmux?") {
		cfg.LaunchTmux = true
		log.Println("✅ tmux launch enabled")
	}

	log.Println()

	// Docker-in-Docker
	if config.AskYesNo("Enable Docker-in-Docker (inner containers, Claude-accessible)?") {
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
		if config.AskYesNo("Workspace doesn't exist. Create it?") {
			os.MkdirAll(workspace, 0755)
			log.Printf("Created workspace: %s\n", workspace)
		}
	}

	if err := config.WriteConfig(projectDir, cfg); err != nil {
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
	seedPath := filepath.Join(config.AssetsDir(), "allowlist-proxy", "allowed-domains.txt")
	plaintextPath := filepath.Join(config.SandclaudeDir(), "allowed-domains.txt")
	encPath := filepath.Join(config.SandclaudeDir(), "allowed-domains.txt.enc")

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

	key, err := creds.AllowlistDeriveKey(keyHex)
	if err != nil {
		return err
	}

	ciphertext, err := creds.AllowlistEncrypt(key, plaintext)
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
	config.EnsureGitignored(".sandclaude/")

	return nil
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

	sc, err := container.NewSandClaude()
	if err != nil {
		return err
	}

	sc.DisableFirewall = disableFirewall
	sc.PassthroughFirewallAndWrite = passthroughFirewallAndWrite
	sc.DisableDind = disableDind
	sc.DevMode = devMode
	return sc.Run(keepDevfiles)
}

// cmdList shows the ./project/config.json
func cmdList() error {
	projectDir := config.GetProjectDir()

	cfg, err := config.ReadConfig(projectDir)
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
		log.Printf("  DinD data: docker volume %s\n", config.DindVolumeName(cfg.Workspace))
	}
	if cfg.ProxyEnabled {
		log.Println("  Proxy:     enabled")
	}
	log.Println()

	return nil
}

// cmdFirewallMonitor tails the allowlist proxy log inside the running container
func cmdFirewallMonitor() error {
	containerName := session.RunningContainerName()

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
	projectDir := config.GetProjectDir()

	workspace := projectDir // fallback
	if cfg, err := config.ReadConfig(projectDir); err == nil {
		workspace = cfg.Workspace
	}

	// Ensure image exists
	sc, err := container.NewSandClaude()
	if err != nil {
		return err
	}

	imageName := "sandclaude-stable"
	if err := sc.EnsureImage(imageName); err != nil {
		return err
	}

	// The real GitHub token is never passed into this debug shell. A dummy
	// GH_TOKEN keeps gh/git from prompting for login (real auth, when the proxy is
	// up, happens at the proxy; the in-container `gh` wrapper blocks `gh auth token`).
	args := []string{
		"run", "--rm", "-it",
		"--cap-add=NET_ADMIN",
		"--cap-add=NET_RAW",
		"-e", "GH_TOKEN=ghp_" + strings.Repeat("0", 36),
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
		"--entrypoint", "/bin/bash",
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
		projectDir := config.GetProjectDir()
		cfg, cfgErr := config.ReadConfig(projectDir)
		if cfgErr != nil {
			log.Printf("Warning: could not read config to derive DinD volume name: %v", cfgErr)
		} else {
			volName := config.DindVolumeName(cfg.Workspace)
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
		legacyDir := filepath.Join(config.GetProjectDir(), "dind-data")
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
	buildArgs = append(buildArgs, "-t", "sandclaude-stable", config.AssetsDir())
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
}

// cmdPopulateProxyCredentials populates proxy-credentials.json interactively using
// claude setup-token. By default it writes the global file (~/.sandclaude/proxy-credentials.json)
// shared across all projects; with projectScope=true it writes the per-project override
// (<cwd>/.sandclaude/project/proxy-credentials.json), which takes precedence per-domain.
func cmdPopulateProxyCredentials(projectScope bool) error {
	var credsPath string
	if projectScope {
		projectDir := config.GetProjectDir()
		if _, err := os.Stat(projectDir); os.IsNotExist(err) {
			return fmt.Errorf("no project found — run: sandclaude init")
		}
		credsPath = creds.ProjectCredentialsPath()
		fmt.Printf("Writing project-specific credentials to: %s\n\n", credsPath)
	} else {
		if err := os.MkdirAll(config.SandclaudeHome(), 0700); err != nil {
			return fmt.Errorf("failed to create %s: %w", config.SandclaudeHome(), err)
		}
		credsPath = creds.GlobalCredentialsPath()
		fmt.Printf("Writing global credentials to: %s\n\n", credsPath)
	}

	// If real credentials already exist, confirm before overwriting
	if !creds.HasOnlyDummyCredentials(credsPath) {
		fmt.Println("⚠️  proxy-credentials.json already contains real credentials.")
		if !config.AskYesNo("Are you sure you want to replace them?") {
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
	} else if config.AskYesNo("Populate Claude credentials?") {
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
	} else if config.AskYesNo("Populate GitHub credentials?") {
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
	scDir := config.SandclaudeDir()

	// Check if project exists
	if _, err := os.Stat(config.GetProjectDir()); os.IsNotExist(err) {
		return fmt.Errorf("no project found at %s", config.GetProjectDir())
	}

	// Confirm deletion
	log.Printf("Warning: This will permanently delete the sandclaude directory\n")
	log.Printf("   Location: %s\n", scDir)
	log.Println("   (config, allowlist, encryption key, and logs)")
	log.Println()

	if !config.AskYesNo("Are you sure you want to remove this project?") {
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

// cmdFirewallReload encrypts allowed-domains.txt → allowed-domains.txt.enc
// using the key from project/.allowlist-key, and reloads the running proxy.
func cmdFirewallReload() error {
	if err := creds.SyncEncryptedAllowlist(); err != nil {
		return err
	}

	// If a container is running, reload the proxy so it picks up the new allowlist.
	containerName := session.RunningContainerName()
	checkCmd := exec.Command("docker", "inspect", "--format={{.State.Running}}", containerName)
	if out, err := checkCmd.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		if err := proxy.ReloadProxyInContainer(containerName); err != nil {
			log.Printf("Warning: proxy reload failed (proxy may not be running yet): %v", err)
		} else {
			log.Printf("✅ Reloaded allowlist-proxy in container '%s'", containerName)
		}
	} else {
		log.Printf("✅ No running container found — encrypted file ready for next start")
	}

	return nil
}

// cmdCapture prints the last 100 lines of the detached session's terminal output.
func cmdCapture() error {
	session, _, err := session.DetachedSessionName()
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
	session, _, err := session.DetachedSessionName()
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
	session, _, err := session.DetachedSessionName()
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
	fmt.Println("  version                  Print version, commit, and build date")
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
			config.DebugMode = true
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}
	os.Args = filteredArgs

	if config.DebugMode {
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
		err = proxy.CmdMonitor(os.Args[2:])

	case "mitm-ports":
		err = proxy.CmdMitmPorts(os.Args[2:])

	case "set-cred":
		err = proxy.CmdSetCred(os.Args[2:])

	case "unset-cred":
		err = proxy.CmdUnsetCred(os.Args[2:])

	case "proxy-apply":
		err = proxy.CmdProxyApply()

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
		err = dashboard.CmdDashboard(os.Args[2:])

	case "dashboard-serve": // internal only, spawned by `sandclaude dashboard`
		err = dashboard.CmdDashboardServe(os.Args[2:])

	case "version", "--version", "-v":
		fmt.Println(config.VersionString())
		return

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
