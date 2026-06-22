# Plan: Closed-Loop Local Development for sandclaude

## Context

`sandclaude start` runs `docker run --rm -it sandclaude-stable`, which requires an interactive TTY on the host. When the outer Claude Code process (the AI assistant) tries to execute `sandclaude start` as a bash command, it fails immediately — `docker run -it` sees no TTY and errors out.

The goal is to close the loop: the outer Claude should be able to spawn a sandclaude container, observe what the inner Claude is doing, send it prompts, and check container status — all without needing an interactive terminal at invocation time. The solution is to auto-detect TTY availability and, when absent, launch the container inside a detached tmux session so tmux provides the PTY.

---

## Approach: Auto-detect TTY, wrap in detached tmux when absent

When `os.Stdin` is a TTY → behave exactly as today (interactive, blocking).  
When `os.Stdin` is not a TTY → create a detached tmux session on the host, run the docker command inside it, print the session name and interaction instructions, return immediately.

No new flags. No config changes. Existing interactive behavior is completely unchanged.

---

## Changes to `main.go`

### 1. Add `detachedSession string` to `SandClaude` struct (line 54–66)

```go
type SandClaude struct {
    ...existing fields...
    detachedSession string  // non-empty when container launched in background tmux session
}
```

### 2. Add `stdinIsTTY()` helper

Pure stdlib, no new imports needed:

```go
func stdinIsTTY() bool {
    fi, err := os.Stdin.Stat()
    if err != nil {
        return false
    }
    return (fi.Mode() & os.ModeCharDevice) != 0
}
```

### 3. Add `shellQuote()` + `buildShellCommand()` helpers

Used to safely pass docker args (which contain paths with spaces, colons, `=`, `$`) as a single shell string to tmux. Equivalent to Python's `shlex.quote()`:

```go
func shellQuote(s string) string {
    return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildShellCommand(parts []string) string {
    quoted := make([]string, len(parts))
    for i, p := range parts {
        quoted[i] = shellQuote(p)
    }
    return strings.Join(quoted, " ")
}
```

### 4. Modify `startDocker` execution block (lines 600–610)

**Before:**
```go
args = append(args, imageName)
debugf("Docker command: docker %s", strings.Join(args, " "))
dockerCmd := exec.Command("docker", args...)
dockerCmd.Stdin = os.Stdin
dockerCmd.Stdout = os.Stdout
dockerCmd.Stderr = os.Stderr
return dockerCmd.Run()
```

**After:**
```go
args = append(args, imageName)
debugf("Docker command: docker %s", strings.Join(args, " "))

if stdinIsTTY() {
    dockerCmd := exec.Command("docker", args...)
    dockerCmd.Stdin = os.Stdin
    dockerCmd.Stdout = os.Stdout
    dockerCmd.Stderr = os.Stderr
    return dockerCmd.Run()
}
return sc.startDetached(containerName, args)
```

Note: line 296 (`args := []string{"run", "--rm", "-it", "--name", containerName}`) does **not** change — `-it` stays in the args because tmux provides the PTY for docker in both paths.

### 5. Add `startDetached` method

```go
func (sc *SandClaude) startDetached(containerName string, args []string) error {
    sessionName := strings.ReplaceAll(containerName, "_", "-")

    // Kill existing session if present
    if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
        log.Printf("Killing existing tmux session '%s'", sessionName)
        if err := exec.Command("tmux", "kill-session", "-t", sessionName).Run(); err != nil {
            return fmt.Errorf("failed to kill existing session '%s': %w", sessionName, err)
        }
    }

    // Build shell-safe docker command string for tmux to execute
    parts := append([]string{"docker"}, args...)
    dockerCmdStr := buildShellCommand(parts)
    debugf("Detached tmux command: tmux new-session -d -s %s %q", sessionName, dockerCmdStr)

    if err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, dockerCmdStr).Run(); err != nil {
        return fmt.Errorf("failed to create tmux session '%s': %w\n\nIs tmux installed?", sessionName, err)
    }

    sc.detachedSession = sessionName

    fmt.Printf("\nsandclaude started in tmux session: %s\n\n", sessionName)
    fmt.Printf("Capture output:      tmux capture-pane -t %s -p\n", sessionName)
    fmt.Printf("Send prompt:         tmux send-keys -t %s 'your prompt' Enter\n", sessionName)
    fmt.Printf("Check container:     docker ps --filter name=%s\n", containerName)
    fmt.Printf("Attach (for user):   tmux attach-session -t %s\n\n", sessionName)
    return nil
}
```

### 6. Fix proxy lifetime in `Run()` (lines 692–698)

When running detached, `Run()` must not kill the proxy immediately on return (the container is still running in the background and needs it).

**Before:**
```go
err = sc.startDocker(cfg, keepDevfiles)

if sc.proxyEnabled {
    sc.stopProxy()
}
```

**After:**
```go
err = sc.startDocker(cfg, keepDevfiles)

if sc.proxyEnabled {
    if sc.detachedSession == "" {
        sc.stopProxy()
    } else {
        log.Printf("Note: mitmproxy is still running alongside the detached container. Stop it manually when done.")
    }
}
```

### 7. Add three helper subcommands

All three derive the session name from the project config (same derivation as `startDetached`): `sandclaude_<workspace-basename>` → `sandclaude-<workspace-basename>`.

**`cmdCapture()`** — prints the last 100 lines of the inner Claude's terminal:
```go
func cmdCapture() error {
    cfg, err := readConfig(getProjectDir())
    if err != nil { return err }
    sessionName := strings.ReplaceAll("sandclaude_"+filepath.Base(cfg.Workspace), "_", "-")
    cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-100")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

**`cmdSend(args []string)`** — sends a prompt to the inner Claude:
```go
func cmdSend(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("usage: sandclaude send <prompt>")
    }
    cfg, err := readConfig(getProjectDir())
    if err != nil { return err }
    sessionName := strings.ReplaceAll("sandclaude_"+filepath.Base(cfg.Workspace), "_", "-")
    prompt := strings.Join(args, " ")
    cmd := exec.Command("tmux", "send-keys", "-t", sessionName, prompt, "Enter")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

**`cmdAttach()`** — attaches to the session interactively (for when the user wants to watch/drive):
```go
func cmdAttach() error {
    cfg, err := readConfig(getProjectDir())
    if err != nil { return err }
    sessionName := strings.ReplaceAll("sandclaude_"+filepath.Base(cfg.Workspace), "_", "-")
    cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
    cmd.Stdin = os.Stdin
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

### 8. Wire new commands in `main()` switch (after existing cases ~line 1698)

```go
case "capture":
    err = cmdCapture()

case "send":
    err = cmdSend(os.Args[2:])

case "attach":
    err = cmdAttach()
```

### 9. Update `usage()` with new subcommands

```
  capture                    Print inner Claude's current terminal output
  send <prompt>              Send a prompt to the running inner Claude
  attach                     Attach interactively to the running session
```

---

## Files to Modify

- `/Users/jackrothrock/claude_sandbox/main.go` — all changes above (no other files change)

## What Does NOT Change

- `launcher.py` — the `LAUNCH_TMUX` mechanism is for tmux *inside* the container; orthogonal to this
- `entrypoint.sh`, `Dockerfile`, config schema — untouched
- `cmdShell`, `cmdFirewallMonitor` — already require interactive TTY; unchanged

---

## Closed-Loop Workflow (Post-Implementation)

The outer Claude can now do:

```bash
./sandclaude start
# → detects no TTY → prints: "sandclaude started in tmux session: sandclaude-claude-sandbox"

./sandclaude capture
# → prints last 100 lines of inner Claude's terminal

./sandclaude send "fix the authentication bug in server.go"
# → sends prompt to inner Claude

./sandclaude capture
# → poll for response/progress

docker ps --filter name=sandclaude_claude_sandbox
# → verify container is still alive

./sandclaude attach
# → user attaches interactively to watch / take over
```

---

## Verification

### Build
```bash
export PATH=$PATH:/usr/local/go/bin
go build -o sandclaude .
```

### Test 1: Interactive path unchanged (from a real terminal)
Run `./sandclaude start` from a macOS Terminal or iTerm2 session as usual. It should block and behave identically to before. No regression.

### Test 2: TTY detection unit check
Piped stdin should return false; both cases should here since Claude Code has no TTY:
```bash
echo "" | go run /tmp/test_tty.go   # stdinIsTTY: false
go run /tmp/test_tty.go             # stdinIsTTY: false (inside this container)
```

### Test 3: Detached path — the closed-loop scenario
Run these from Claude Code's Bash tool (which has no TTY):

```bash
# Step 1: Start — should return immediately and print session info
./sandclaude start --disable-firewall
# Expected:
#   sandclaude started in tmux session: sandclaude-claude-sandbox
#   sandclaude capture          # read inner Claude output
#   sandclaude send '<prompt>'  # send a prompt to inner Claude
#   sandclaude attach           # attach interactively

# Step 2: Verify session and container are running
tmux list-sessions
docker ps --filter name=sandclaude_claude_sandbox

# Step 3: Read what inner Claude is showing
./sandclaude capture

# Step 4: Send a prompt
./sandclaude send "list the files in the current directory"

# Step 5: Read the response
sleep 3 && ./sandclaude capture
```

### Test 4: Session collision — restart while running
```bash
./sandclaude start --disable-firewall   # start session
./sandclaude start --disable-firewall   # re-start: should kill old, create new
# Expected log line: "Killing existing tmux session 'sandclaude-claude-sandbox'"
```

### Test 5: Help output includes new commands
```bash
./sandclaude help | grep -E "capture|send|attach"
```
