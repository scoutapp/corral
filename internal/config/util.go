package config

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// DebugMode enables verbose debug logging when set to true via --debug flag.
var DebugMode bool

// Debugf logs a formatted message only when debug mode is enabled.
func Debugf(format string, args ...any) {
	if DebugMode {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Debugln logs a message only when debug mode is enabled.
func Debugln(args ...any) {
	if DebugMode {
		log.Println(append([]any{"[DEBUG]"}, args...)...)
	}
}

// DindVolumeName returns a deterministic Docker named volume for a workspace's
// inner Docker data root. Named volumes sidestep the "lchown /proc: permission
// denied" error that bind mounts hit when Docker extracts layers containing /proc.
func DindVolumeName(workspace string) string {
	h := sha256.Sum256([]byte(workspace))
	return fmt.Sprintf("sandclaude-dind-%x", h[:6])
}

// ShellQuote returns a single-quoted, shell-safe version of s (equivalent to Python's shlex.quote).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BuildShellCommand returns a single shell command string with all parts properly quoted.
func BuildShellCommand(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = ShellQuote(p)
	}
	return strings.Join(quoted, " ")
}

// AskYesNo prompts the user with a yes/no question
func AskYesNo(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// FindFreePort returns the first available TCP port starting from startPort.
func FindFreePort(startPort int) (int, error) {
	Debugf("Scanning for free port starting at %d", startPort)
	for port := startPort; port < startPort+100; port++ {
		// Check both 0.0.0.0 (what mitmproxy uses for --listen-port) and
		// 127.0.0.1 (what mitmweb uses for --web-port). A port is only free
		// if both succeed.
		addr1 := fmt.Sprintf("0.0.0.0:%d", port)
		ln1, err1 := net.Listen("tcp", addr1)
		if err1 != nil {
			Debugf("Port %d unavailable on 0.0.0.0: %v", port, err1)
			continue
		}
		ln1.Close()

		addr2 := fmt.Sprintf("127.0.0.1:%d", port)
		ln2, err2 := net.Listen("tcp", addr2)
		if err2 != nil {
			Debugf("Port %d unavailable on 127.0.0.1: %v", port, err2)
			continue
		}
		ln2.Close()

		Debugf("Found free port: %d", port)
		return port, nil
	}
	return 0, fmt.Errorf("no free port found in range %d-%d", startPort, startPort+99)
}

// IsDirWritable reports whether the current process can create files in dir.
func IsDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// OpenBrowser best-effort opens url in the user's default browser. Launching
// is a convenience on top of the printed URL, never a requirement, so callers
// should treat a returned error as non-fatal (e.g. log at debug level).
//
// Set SANDCLAUDE_NO_BROWSER=1 to suppress the launch entirely — used by the e2e
// suite (and handy for headless/CI use) so `start` doesn't pop a tab per run.
func OpenBrowser(url string) error {
	if os.Getenv("SANDCLAUDE_NO_BROWSER") != "" {
		return nil
	}
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
