package cli

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/dashboard"
	sshagent "github.com/jackrothrock/sandclaude/internal/ssh"
)

// cmdUninstall removes everything sandclaude created on this machine and then
// deletes the sandclaude binary itself (self-removal is last). It is deliberately
// aggressive — the point is to leave no trace — so it requires an explicit typed
// confirmation unless --yes is passed.
//
// What it tears down, in order (each step best-effort; a failure logs a warning
// and we continue so a single stuck resource can't strand the rest):
//  1. dashboard daemon (SIGTERM via its state file)
//  2. tmux sessions       sandclaude_*
//  3. containers          sandclaude_*   (running or stopped, force-removed)
//  4. image               sandclaude-stable
//  5. DinD volumes        sandclaude-dind-*
//  6. scoped ssh-agents   (killed; ~/.sandclaude/agents removed)
//  7. ~/.sandclaude/       (assets, managed workspaces, creds, ssh-keys, logs, state)
//  8. the binary itself    (the currently-running executable)
//
// It intentionally does NOT touch per-project ./.sandclaude/ dirs living inside
// the user's own repositories — those are scattered, may be committed, and
// `sandclaude remove` already handles them one-by-one. We print a note about them.
//
// Flags:
//
//	--yes / -y      skip the confirmation prompt
//	--keep-images   preserve the Docker image + DinD volumes (steps 4 & 5)
func cmdUninstall(args []string) error {
	assumeYes := false
	keepImages := false
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			assumeYes = true
		case "--keep-images":
			keepImages = true
		default:
			return fmt.Errorf("unknown flag for uninstall: %s", a)
		}
	}

	exePath, exeErr := os.Executable()
	if exeErr == nil {
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = resolved
		}
	}
	home := config.SandclaudeHome()

	log.Println("This will remove EVERYTHING sandclaude created on this machine:")
	log.Println("  • the dashboard daemon (stopped)")
	log.Println("  • all sandclaude_* containers and tmux sessions")
	if !keepImages {
		log.Println("  • the sandclaude-stable image and sandclaude-dind-* volumes")
	}
	log.Println("  • all scoped ssh-agents")
	log.Printf("  • %s (assets, managed workspaces, credentials, ssh-keys, logs)\n", home)
	if exeErr == nil {
		log.Printf("  • the sandclaude binary itself (%s)\n", exePath)
	}
	log.Println()
	log.Println("It will NOT touch ./.sandclaude/ directories inside your own repositories.")
	log.Println()

	if !assumeYes {
		if !config.AskYesNo("Are you absolutely sure you want to uninstall sandclaude?") {
			log.Println("Cancelled.")
			return nil
		}
	}

	// 1. Stop the dashboard daemon (best-effort; ignores "not running").
	log.Println("==> Stopping dashboard daemon")
	if err := dashboard.CmdDashboard([]string{"stop"}); err != nil {
		log.Printf("    warning: could not stop dashboard: %v", err)
	}

	// 2. Kill sandclaude_* tmux sessions.
	log.Println("==> Killing tmux sessions")
	killTmuxSessions()

	// 3. Force-remove sandclaude_* containers (running or stopped).
	log.Println("==> Removing containers")
	removeContainers()

	if !keepImages {
		// 4. Remove the sandbox image.
		log.Println("==> Removing image sandclaude-stable")
		if out, err := exec.Command("docker", "rmi", "-f", "sandclaude-stable").CombinedOutput(); err != nil {
			// Not fatal — the image may simply not exist.
			if !strings.Contains(string(out), "No such image") {
				log.Printf("    warning: docker rmi sandclaude-stable: %v", strings.TrimSpace(string(out)))
			}
		}

		// 5. Remove DinD volumes (sandclaude-dind-*).
		log.Println("==> Removing DinD volumes")
		removeDindVolumes()
	}

	// 6. Kill scoped ssh-agents and remove the agents root.
	log.Println("==> Stopping scoped ssh-agents")
	n := sshagent.StopAll()
	log.Printf("    stopped %d ssh-agent(s)", n)

	// 7. Remove ~/.sandclaude entirely (assets, workspaces, creds, state, logs).
	log.Printf("==> Removing %s", home)
	if err := os.RemoveAll(home); err != nil {
		log.Printf("    warning: could not remove %s: %v", home, err)
	}

	// 8. Remove the binary itself — LAST, so a failure above doesn't leave a
	// half-uninstalled system with no CLI to retry with. On macOS/Linux a running
	// executable can delete its own on-disk file (the inode stays live until this
	// process exits), so this is safe.
	if exeErr != nil {
		log.Printf("Warning: could not resolve the sandclaude binary path (%v) — remove it manually.", exeErr)
	} else {
		log.Printf("==> Removing binary %s", exePath)
		if err := removeSelf(exePath); err != nil {
			log.Printf("    could not remove %s automatically: %v", exePath, err)
			log.Printf("    remove it manually: sudo rm %s", exePath)
		}
	}

	log.Println()
	log.Println("✅ sandclaude uninstalled.")
	log.Println("   Note: any ./.sandclaude/ directories inside your own repos remain —")
	log.Println("   delete them per-project with `rm -rf .sandclaude` if you want them gone.")
	return nil
}

// removeSelf deletes the binary at path, falling back to sudo when the install
// dir isn't user-writable (mirrors install.sh's /usr/local/bin sudo behavior).
func removeSelf(path string) error {
	if err := os.Remove(path); err == nil {
		return nil
	}
	// Not writable directly — try sudo (interactive; prompts on the controlling TTY).
	dir := filepath.Dir(path)
	if !dirWritable(dir) {
		cmd := exec.Command("sudo", "rm", "-f", path)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	// Directory looked writable but Remove still failed — surface the real error.
	return os.Remove(path)
}

// dirWritable reports whether dir is writable by this user (cheap heuristic via a
// temp file; used to decide whether removing the binary needs sudo).
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".sandclaude-wtest-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// killTmuxSessions kills every tmux session named sandclaude_*.
func killTmuxSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return // no tmux server running, or tmux absent — nothing to do
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "sandclaude_") {
			_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		}
	}
}

// removeContainers force-removes every container named sandclaude_* (running or
// stopped). Matches on the name prefix so all per-workspace containers are caught.
func removeContainers() {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "name=^/sandclaude_").Output()
	if err != nil {
		log.Printf("    warning: could not list containers (is docker running?): %v", err)
		return
	}
	ids := strings.Fields(strings.TrimSpace(string(out)))
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rm", "-f"}, ids...)
	if err := exec.Command("docker", args...).Run(); err != nil {
		log.Printf("    warning: docker rm -f failed for some containers: %v", err)
	}
}

// removeDindVolumes removes every Docker volume named sandclaude-dind-*.
func removeDindVolumes() {
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=sandclaude-dind-").Output()
	if err != nil {
		log.Printf("    warning: could not list volumes: %v", err)
		return
	}
	vols := strings.Fields(strings.TrimSpace(string(out)))
	if len(vols) == 0 {
		return
	}
	args := append([]string{"volume", "rm", "-f"}, vols...)
	if err := exec.Command("docker", args...).Run(); err != nil {
		log.Printf("    warning: docker volume rm failed for some volumes: %v", err)
	}
}
