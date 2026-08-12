package container

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/creds"
	"github.com/scoutapp/corral/internal/proxy"
	"github.com/scoutapp/corral/internal/session"
	sshagent "github.com/scoutapp/corral/internal/ssh"
)

// startDocker starts the Docker container with Claude Code
func (sc *Corral) startDocker(cfg *config.ProjectConfig, keepDevfiles bool) error {
	workspace := cfg.Workspace
	// Build image if needed
	imageName := "corral-stable"
	if err := sc.EnsureImage(imageName); err != nil {
		return err
	}

	log.Printf("Starting corral (workspace: %s)", workspace)

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Build docker args
	containerName := session.ContainerNameForWorkspace(workspace)
	args := []string{"run", "--rm", "-it", "--name", containerName}

	// DinD requires --privileged (superset of NET_ADMIN + NET_RAW + SYS_ADMIN).
	// Without DinD, use minimal capabilities.
	if sc.dindEnabled {
		args = append(args, "--privileged")
	} else {
		args = append(args, "--cap-add=NET_ADMIN", "--cap-add=NET_RAW")
		// Seccomp only bites without --privileged (DinD's --privileged already
		// disables it). "" / "default" -> Docker's default profile (omit the flag);
		// "unconfined" or a custom profile path -> pass --security-opt seccomp=…
		// (e.g. Erlang/BEAM needs syscalls the default profile blocks).
		if m := sc.seccompMode; m != "" && m != "default" {
			args = append(args, "--security-opt", "seccomp="+m)
			log.Printf("seccomp: %s", m)
		}
	}

	if sc.DisableFirewall {
		args = append(args, "-e", "DISABLE_FIREWALL=1")
	} else if sc.PassthroughFirewallAndWrite {
		args = append(args, "-e", "DISABLE_FIREWALL_AND_WRITE=1")
		// Still need the allowlist key and file for the proxy to start
		projectDir := config.GetProjectDir()
		keyPath := filepath.Join(projectDir, ".allowlist-key")
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("encryption key not found at %s\nRun 'corral init' to generate it", keyPath)
		}
		args = append(args, "-e", fmt.Sprintf("ALLOWLIST_KEY=%s", strings.TrimSpace(string(keyData))))

		encPath := filepath.Join(config.CorralDir(), "allowed-domains.txt.enc")
		if _, err := os.Stat(encPath); os.IsNotExist(err) {
			return fmt.Errorf("encrypted allowlist not found at %s\nRun 'corral firewall-reload' to create it", encPath)
		}
		os.Chmod(encPath, 0644)
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/allowed-domains.txt.enc:ro", encPath))
	} else {
		// Read encryption key from project
		projectDir := config.GetProjectDir()
		keyPath := filepath.Join(projectDir, ".allowlist-key")
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("encryption key not found at %s\nRun 'corral init' to generate it", keyPath)
		}
		args = append(args, "-e", fmt.Sprintf("ALLOWLIST_KEY=%s", strings.TrimSpace(string(keyData))))

		// Mount the encrypted allowlist file
		encPath := filepath.Join(config.CorralDir(), "allowed-domains.txt.enc")
		if _, err := os.Stat(encPath); os.IsNotExist(err) {
			return fmt.Errorf("encrypted allowlist not found at %s\nRun 'corral firewall-reload' to create it", encPath)
		}
		// Make sure the file is world-readable so proxyuser can read it
		os.Chmod(encPath, 0644)
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/allowed-domains.txt.enc:ro", encPath))
	}

	// Claude auth: in proxy mode, generate dummy token; otherwise let Claude handle auth
	if sc.proxyEnabled {
		config.Debugln("Generating dummy auth token for proxy mode (proxy will inject real credentials)")
		dummyToken := "sk-ant-oat01-" + strings.Repeat("0", 86) + "-" + strings.Repeat("0", 8)
		args = append(args, "-e", fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", dummyToken))
	}
	// In non-proxy mode, Claude Code will handle its own authentication

	// GitHub auth mirrors Claude's own token handling above:
	//   - Proxy mode: pass a DUMMY GH_TOKEN. gh/git need *some* token present or
	//     they prompt for login, but the real token never enters the container —
	//     the host credential proxy injects it into api.github.com requests, and
	//     the in-container `gh` wrapper blocks `gh auth token`. So `echo $GH_TOKEN`
	//     only ever reveals the dummy.
	//   - Non-proxy mode: the user opted out of credential protection; pass the
	//     real token so gh works (there is no injector).
	if sc.proxyEnabled {
		config.Debugln("Proxy mode: injecting dummy GH_TOKEN (real GitHub creds injected by the proxy)")
		args = append(args, "-e", "GH_TOKEN=ghp_"+strings.Repeat("0", 36))
	} else if output, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if ghToken := strings.TrimSpace(string(output)); ghToken != "" {
			config.Debugln("Non-proxy mode: passing real GH_TOKEN to container")
			args = append(args, "-e", fmt.Sprintf("GH_TOKEN=%s", ghToken))
		}
	}

	// Mount the workspace and set it as the working dir.
	args = append(args,
		"-v", fmt.Sprintf("%s:%s", workspace, workspace),
		"-w", workspace,
	)

	// Scoped ssh-agent: when a per-project agent was started (keys chosen for this
	// project), bind-mount ONLY its socket and point SSH_AUTH_SOCK at it. No key
	// file is ever mounted — the container can sign via the agent but cannot read
	// the key bytes. See internal/ssh and docs/security.md.
	if sc.sshAgent != nil {
		args = append(args,
			"-v", fmt.Sprintf("%s:%s", sc.sshAgent.SocketPath, sshagent.ContainerSocketPath),
			"-e", "SSH_AUTH_SOCK="+sshagent.ContainerSocketPath,
		)
	}

	// Mount host ~/.claude.json (Claude Code config file) ONLY if it exists as a
	// file. Docker auto-creates a missing bind source as a DIRECTORY, which then
	// fails the container with a cryptic OCI "not a directory" mount error when
	// the target is a file. A host that hasn't run `claude` auth yet simply has
	// no ~/.claude.json — skip the mount and let Claude prompt inside the
	// container. (We don't mount the whole ~/.claude dir here; that's done below
	// with granular per-subdir mounts.)
	hostClaudeJSON := filepath.Join(home, ".claude.json")
	if fi, err := os.Stat(hostClaudeJSON); err == nil && !fi.IsDir() {
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/.claude.json", hostClaudeJSON))
	}

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
			config.Debugf("Mounting %s .claude/%s/* (%d items, read-only)", sourceLabel, subName, len(entries))
			for _, entry := range entries {
				entrySrc := filepath.Join(srcSubDir, entry.Name())
				entryDst := fmt.Sprintf("/home/claude/.claude/%s/%s", subName, entry.Name())
				if mountedDsts[entryDst] {
					config.Debugf("  skip (already mounted): %s", entryDst)
					continue
				}
				mountedDsts[entryDst] = true
				config.Debugf("  volume: %s -> %s:ro", entrySrc, entryDst)
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

	// The sandbox bundle ships skills at <assets>/skills (they land at
	// ~/.claude/skills in the image); register it as a .claude subdir so the
	// mount plan below treats it like any other.
	if _, err := os.Stat(filepath.Join(config.AssetsDir(), "skills")); err == nil {
		allSubdirs["skills"] = true
	}

	workspaceClaudeDir := filepath.Join(workspace, ".claude")
	workspaceCorralDir := filepath.Join(workspace, ".corral")
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
			// The sandbox bundle stores skills at <assets>/skills (no .claude
			// wrapper), so pass AssetsDir() as the "sourceClaudeDir" — join with
			// subName ("skills") yields <assets>/skills.
			mountClaudeSubdirItems("sandbox", config.AssetsDir(), subName)
			mountClaudeSubdirItems("workspace", workspaceClaudeDir, subName)

			// Also check .corral for project-specific items (primarily for skills)
			if subName == "skills" {
				mountClaudeSubdirItems("workspace/.corral", workspaceCorralDir, subName)
			}
		} else if writableSubdirs[subName] {
			// Writable subdirectories: mount from host with read-write access for persistence
			hostSubdir := filepath.Join(hostClaudeDir, subName)
			if _, err := os.Stat(hostSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				config.Debugf("volume: host .claude/%s -> %s:rw", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:rw", hostSubdir, dst))
			}
		} else {
			// Other subdirectories: mount read-only from workspace if it exists, otherwise from host
			workspaceSubdir := filepath.Join(workspaceClaudeDir, subName)
			hostSubdir := filepath.Join(hostClaudeDir, subName)

			if _, err := os.Stat(workspaceSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				config.Debugf("volume: workspace .claude/%s -> %s:ro", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", workspaceSubdir, dst))
			} else if _, err := os.Stat(hostSubdir); err == nil {
				dst := fmt.Sprintf("/home/claude/.claude/%s", subName)
				config.Debugf("volume: host .claude/%s -> %s:ro", subName, dst)
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", hostSubdir, dst))
			}
		}
	}

	// Mount empty tmpfs over .devcontainer to hide it from the container (unless --keep-devfiles)
	if !keepDevfiles {
		args = append(args, "--tmpfs", fmt.Sprintf("%s/.devcontainer:rw,noexec,nosuid,size=1m", workspace))
	}

	// The plaintext allowlist always lives at <cwd>/.corral/allowed-domains.txt,
	// owned by the project. The container appends newly-seen domains here in
	// passthrough-and-write mode.
	allowlistPath := filepath.Join(config.CorralDir(), "allowed-domains.txt")
	if sc.PassthroughFirewallAndWrite {
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
	if cfg, err := config.ReadConfig(config.GetProjectDir()); err == nil {
		// Resolve the capture preset (minimal/all/none/custom) to the effective
		// monitor-host list. Write the file UNCONDITIONALLY (even when empty) and
		// always mount it: if we skipped the mount/file, Docker's -v with a missing
		// source auto-creates the target as a DIRECTORY, which then breaks every
		// later WriteMonitorHostsFile (and the reload). An empty file means "monitor
		// all" (the proxy treats empty == absent). If a prior run already left a
		// directory there, remove it first.
		monitorPath := proxy.MonitorHostsPath()
		if fi, statErr := os.Stat(monitorPath); statErr == nil && fi.IsDir() {
			os.RemoveAll(monitorPath)
		}
		if err := config.WriteMonitorHostsFile(monitorPath, cfg.ResolveMonitorHosts()); err != nil {
			return fmt.Errorf("failed to write monitor-hosts file: %w", err)
		}
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/monitor-hosts.txt:rw", monitorPath))
		args = append(args, "-e", "CORRAL_MITM_PORTS="+strings.Join(cfg.MitmPortsOrDefault(), ","))

		// Credential-hosts: hosts with an injected credential are ALWAYS mitm'd,
		// independent of the monitor-list (the proxy force-mitms them). Write just
		// the hostnames (never the secret values) and mount them for --credential-hosts.
		credHostsPath := filepath.Join(config.GetProjectDir(), "credential-hosts.txt")
		if err := creds.WriteCredentialHostsFile(credHostsPath, creds.CredentialHostnames()); err != nil {
			return fmt.Errorf("failed to write credential-hosts file: %w", err)
		}
		args = append(args, "-v", fmt.Sprintf("%s:/home/claude/credential-hosts.txt:rw", credHostsPath))
	}

	logsDir := config.GetLogsDir()
	os.MkdirAll(logsDir, 0755)
	args = append(args, "-v", fmt.Sprintf("%s:/home/claude/logs", logsDir))

	// Mount the bin scripts from the asset bundle so they can be edited without
	// rebuilding. They live in two tier dirs — setup/ (sandbox self-config) and
	// dind/ (the sandbox->inner bridge) — but both flatten into /home/claude/bin
	// to match the flat in-container path everything references. Mount each script
	// individually (rather than one dir mount) so the two source dirs coexist on
	// the same target without shadowing each other.
	for _, tier := range []string{"setup", "dind"} {
		tierDir := filepath.Join(config.AssetsDir(), tier)
		entries, err := os.ReadDir(tierDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src := filepath.Join(tierDir, e.Name())
			dst := fmt.Sprintf("/home/claude/bin/%s", e.Name())
			args = append(args, "-v", fmt.Sprintf("%s:%s", src, dst))
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
			config.Debugln("Proxy CA cert found on host, mounting into container")
		} else {
			config.Debugln("Proxy CA cert not on host — will be generated when proxy starts")
		}
	}

	// Mark the container as a corral environment so nested `corral start`
	// calls can detect they're already inside and skip the docker layer entirely.
	args = append(args, "-e", "CORRAL_CONTAINER=1")

	// DinD: signal entrypoint to start inner dockerd and expose ports
	if sc.dindEnabled {
		args = append(args, "-e", "DIND_ENABLED=1")

		// Use a named volume for the inner Docker data root.
		// Bind mounts fail with "lchown /proc: permission denied" when Docker
		// tries to extract image layers that contain /proc entries — named
		// volumes avoid this because Docker manages ownership internally.
		dindVol := config.DindVolumeName(cfg.Workspace)
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

	config.Debugf("Docker command: docker %s", strings.Join(args, " "))

	// `dev` launches the container in a detached host-level tmux session for closed-loop
	// development (observe/drive via capture/send/attach). Plain `start` always runs
	// interactively, attached to the current terminal.
	if sc.DevMode {
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
func (sc *Corral) startDetached(containerName string, args []string) error {
	sessionName := session.TmuxSessionNameForContainer(containerName)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		log.Printf("Killing existing tmux session '%s'", sessionName)
		if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil {
			return fmt.Errorf("failed to kill existing tmux session '%s': %w", sessionName, err)
		}
	}

	parts := append([]string{"docker"}, args...)
	dockerCmdStr := config.BuildShellCommand(parts)
	config.Debugf("Detached tmux command: tmux new-session -d -s %s %q", sessionName, dockerCmdStr)

	// -x/-y force an explicit pane size so the pane gets a real PTY even when the
	// tmux server has no controlling terminal (headless CI/SSH). Without a sized
	// pane the PTY can be unusable, and `docker run -it` then exits immediately —
	// which also tears the whole session down ("no server running"), leaving no
	// container and nothing to debug.
	//
	// `set-option -g remain-on-exit on`, chained into the SAME invocation so it
	// applies before the pane can die, keeps the pane (and thus the session)
	// around after its process exits — so a failed `docker run` is inspectable via
	// capture-pane instead of vanishing. Belt-and-suspenders with the sizing fix.
	// Deliberately NO `mouse on` for the Claude session, and `status off`, so it
	// reads as a plain terminal:
	//   • mouse off — with mouse ON, tmux captures the scroll wheel and the drag
	//     (entering its copy-mode), which breaks native browser drag-select/copy in
	//     the dashboard terminal AND interferes with the container app's
	//     alternate-screen switch (leaving boot logs above Claude's TUI). With it
	//     off, the xterm client owns scroll (its own 10k-line scrollback scrolls on
	//     the wheel) and selection (native drag-to-copy); tmux is just the
	//     detach/attach plumbing.
	//   • status off — hide tmux's status bar. Otherwise the default (status on)
	//     shows a tmux badge in the corner (incl. the copy-mode "[0/180]" scroll
	//     indicator) — the "tmux sign" that shouldn't appear on a plain terminal.
	// (The HOST shell already sets status off + mouse on + split; see terminal.go.)
	newSession := exec.Command(
		"tmux", "new-session", "-d", "-x", "200", "-y", "50", "-s", sessionName, dockerCmdStr,
		";", "set-option", "-g", "remain-on-exit", "on",
		";", "set-option", "-t", sessionName, "status", "off",
		// Forward an inner app's OSC 52 clipboard write out to the browser/xterm.js
		// client so copy inside Claude reaches the system clipboard. set-clipboard
		// on makes tmux emit the OSC 52; the terminal-overrides Ms entry forces the
		// "set clipboard" capability on for every client (the daemon's PTY TERM
		// wouldn't reliably carry it). xterm.js handles the forwarded sequence.
		";", "set-option", "-t", sessionName, "set-clipboard", "on",
		";", "set-option", "-t", sessionName, "-a", "terminal-overrides", ",*:Ms=\\E]52;%p1%s;%p2%s\\007",
	)
	if out, err := newSession.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tmux session '%s': %w\n%s\n\nIs tmux installed?", sessionName, err, string(out))
	}

	sc.detachedSession = sessionName

	fmt.Printf("\ncorral started in tmux session: %s\n\n", sessionName)
	fmt.Printf("  corral capture          # read inner Claude output\n")
	fmt.Printf("  corral send '<prompt>'  # send a prompt to inner Claude\n")
	fmt.Printf("  corral attach           # attach interactively\n")
	fmt.Printf("  docker ps --filter name=%s\n\n", containerName)
	return nil
}

// EnsureImage builds the Docker image if it doesn't exist
func (sc *Corral) EnsureImage(imageName string) error {
	// Check if image exists
	cmd := exec.Command("docker", "image", "inspect", imageName)
	if err := cmd.Run(); err == nil {
		config.Debugf("Image '%s' already exists, skipping build", imageName)
		// Support images built by older CLIs: don't force a rebuild, just warn so
		// the user knows a `corral update` (or `rebuild`) would refresh it.
		if stale, imgVer := ImageStale(); stale {
			log.Printf("⚠️  Image %s was built by corral %s but this CLI is %s — "+
				"run `corral update` (or `corral rebuild`) to refresh it.",
				imageName, imgVer, config.Version)
		}
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

	// Build arg list: user IDs + Playwright skip. This is the OUTER sandbox image
	// build, running on the HOST's docker daemon BEFORE any sandbox/proxy exists —
	// so it must NOT be pointed at the in-sandbox allowlist proxy (172.18.0.1:3128).
	// That address is unreachable here; hardcoding it broke cold builds (e.g. a
	// fresh CI runner) where apt in the Dockerfile timed out against a dead proxy.
	// Inner DinD build containers DO use 172.18.0.1:3128 — see the separate build
	// path there. Here we simply pass through the host's own proxy env if it has
	// one (nested-sandbox case); otherwise the build uses direct host networking.
	buildArgs := []string{
		"build",
		"--build-arg", fmt.Sprintf("USER_ID=%s", userID),
		"--build-arg", fmt.Sprintf("GROUP_ID=%s", groupID),
		// Skip the ~200MB Chromium binary download at build time; it can be
		// installed after the image is running if needed.
		"--build-arg", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
	}
	// Propagate the host's proxy env only when actually set (e.g. corral
	// running inside another sandbox). Empty otherwise -> direct networking.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v := os.Getenv(key); v != "" {
			buildArgs = append(buildArgs, "--build-arg", fmt.Sprintf("%s=%s", key, v))
		}
	}

	// The allowlist proxy does TLS interception (MITM), so build containers must
	// trust its CA cert for HTTPS to work. Read the cert and pass it base64-encoded.
	if certBytes, err := os.ReadFile("/etc/proxy-ca.crt"); err == nil {
		buildArgs = append(buildArgs,
			"--build-arg", fmt.Sprintf("PROXY_CA_CERT=%s", base64.StdEncoding.EncodeToString(certBytes)),
		)
	}

	// Stamp the build with the CLI version (label + version-pinned tag) so a later
	// run can detect an image built by an older CLI. imageBuildTags applies the
	// canonical `corral-stable` tag plus the :<version> alias for real releases.
	buildArgs = append(buildArgs, imageVersionLabelArg()...)
	buildArgs = append(buildArgs, imageBuildTags()...)
	buildArgs = append(buildArgs, config.AssetsDir())
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	return buildCmd.Run()
}

// startDirect launches Claude directly inside the current corral container.
// Used when corral start is called from within a running corral container
// (detected via CORRAL_CONTAINER=1). The proxy, firewall, and workspace are
// already set up by the outer entrypoint.
//
// We run claude directly rather than via launcher.py to avoid launcher.py trying to
// create a tmux session named "corral" which already exists (it's the outer session).
func (sc *Corral) startDirect(cfg *config.ProjectConfig) error {
	log.Println("Running inside corral container — starting Claude directly (no nested Docker)")

	// patch-claude-settings.py must run before Claude starts.
	patch := exec.Command("python3", "/home/claude/bin/patch-claude-settings.py")
	patch.Stdout = os.Stdout
	patch.Stderr = os.Stderr
	if err := patch.Run(); err != nil {
		log.Printf("Warning: patch-claude-settings.py failed: %v", err)
	}

	if !sc.DevMode {
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
	// We avoid launcher.py because it would try to create a session named "corral"
	// which already exists (the outer session we're running in).
	containerName := session.ContainerNameForWorkspace(cfg.Workspace)
	sessionName := session.TmuxSessionNameForContainer(containerName)

	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		log.Printf("Killing existing tmux session '%s'", sessionName)
		if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil {
			return fmt.Errorf("failed to kill existing tmux session '%s': %w", sessionName, err)
		}
	}

	claudeCmd := "cd " + config.ShellQuote(cfg.Workspace) + " && claude --dangerously-skip-permissions; exec bash"
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, claudeCmd).Run(); err != nil {
		return fmt.Errorf("failed to create tmux session '%s': %w", sessionName, err)
	}

	sc.detachedSession = sessionName

	fmt.Printf("\nClaude started in tmux session: %s\n\n", sessionName)
	fmt.Printf("  corral capture          # read inner Claude output\n")
	fmt.Printf("  corral send '<prompt>'  # send a prompt to inner Claude\n")
	fmt.Printf("  corral attach           # attach interactively\n\n")
	return nil
}
