package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ----------------------------------------------------------------------------
// Project registry (~/.sandclaude/projects.json)
//
// Purely a discovery aid — "which workspace paths has the user ever started a
// project in" — never trusted for liveness. Whether a registered project is
// actually running right now is always re-derived on demand (projectLiveStatus),
// the same way runningContainerName() already re-checks Docker rather than
// caching a status flag. This sidesteps the many unreliable ways a project can
// stop (Ctrl-C, `docker kill`, crash, `sandclaude remove`) — there is no single
// hook to deregister from, so we don't try.
// ----------------------------------------------------------------------------

type ProjectRegistryEntry struct {
	Workspace   string `json:"workspace"`
	LastStarted string `json:"last_started"`
}

type ProjectRegistry struct {
	Projects []ProjectRegistryEntry `json:"projects"`
}

func registryPath() string {
	return filepath.Join(sandclaudeHome(), "projects.json")
}

// readRegistry tolerates a missing file (first run) but not a corrupt one.
func readRegistry() (*ProjectRegistry, error) {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectRegistry{}, nil
		}
		return nil, fmt.Errorf("failed to read project registry: %w", err)
	}
	var reg ProjectRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("invalid projects.json: %w", err)
	}
	return &reg, nil
}

func writeRegistry(reg *ProjectRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sandclaudeHome(), 0700); err != nil {
		return err
	}
	return os.WriteFile(registryPath(), data, 0600)
}

// registerProject upserts a workspace's entry (matched by absolute path) with the
// current timestamp. Called from Run() every time a project starts.
func registerProject(workspace string) error {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		absWorkspace = workspace
	}

	reg, err := readRegistry()
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range reg.Projects {
		if reg.Projects[i].Workspace == absWorkspace {
			reg.Projects[i].LastStarted = now
			return writeRegistry(reg)
		}
	}
	reg.Projects = append(reg.Projects, ProjectRegistryEntry{Workspace: absWorkspace, LastStarted: now})
	return writeRegistry(reg)
}

// ----------------------------------------------------------------------------
// Per-project paths, parameterized by workspace.
//
// Every existing helper (sandclaudeDir/getProjectDir/getLogsDir) resolves via
// os.Getwd(), which is correct for every existing command — they only ever
// operate on "the project you're standing in." The dashboard is host-wide and
// needs to inspect *other* projects regardless of its own cwd, so it needs
// workspace-parameterized siblings. These assume what the rest of the codebase
// already assumes (README: `sandclaude init` is run from the project's own
// directory) — that .sandclaude/ lives directly under the workspace path.
// ----------------------------------------------------------------------------

func projectDirForWorkspace(workspace string) string {
	return filepath.Join(workspace, ".sandclaude", "project")
}

func logsDirForWorkspace(workspace string) string {
	return filepath.Join(workspace, ".sandclaude", "logs")
}

// ----------------------------------------------------------------------------
// Mitm proxy runtime state — how the dashboard discovers a running project's
// mitmweb web-UI port, which is otherwise ephemeral (chosen fresh each start,
// never persisted to config.json).
// ----------------------------------------------------------------------------

type ProxyRuntimeState struct {
	ProxyPort int    `json:"proxy_port"`
	WebPort   int    `json:"web_port"`
	Pid       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

func proxyRuntimeStatePath() string {
	return filepath.Join(getProjectDir(), "runtime.json")
}

// writeProxyRuntimeState is called from startProxy() (cwd == project workspace,
// same assumption getLogsDir() already makes).
func writeProxyRuntimeState(webPort int, pid int) error {
	state := ProxyRuntimeState{
		ProxyPort: 0, // filled in by caller if ever needed; not used by the dashboard today
		WebPort:   webPort,
		Pid:       pid,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(getProjectDir(), 0700); err != nil {
		return err
	}
	return os.WriteFile(proxyRuntimeStatePath(), data, 0600)
}

// readProxyRuntimeStateFor reads another project's runtime state by workspace path.
// Returns (nil, nil) if the file doesn't exist (proxy never started, or already
// cleaned up) rather than an error — absence is a normal, expected state here.
func readProxyRuntimeStateFor(workspace string) (*ProxyRuntimeState, error) {
	path := filepath.Join(projectDirForWorkspace(workspace), "runtime.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state ProxyRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// dockerContainerRunning reports whether a container with the given name is
// currently running. Mirrors the same `docker inspect --format={{.State.Running}}`
// check already used at cmdFirewallReload (main.go) and cmdFirewallMonitor's
// existence check, factored here so the dashboard doesn't duplicate it a third time.
func dockerContainerRunning(containerName string) bool {
	out, err := exec.Command("docker", "inspect", "--format={{.State.Running}}", containerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// tmuxSessionExists mirrors the `tmux has-session` check already used in
// startDetached/startDirect.
func tmuxSessionExists(sessionName string) bool {
	return exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil
}

// pidAlive reports whether a process with the given pid currently exists.
// Signal 0 performs no actual signal delivery, just existence/permission checks.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// ----------------------------------------------------------------------------
// Live status — always re-derived from Docker/tmux/the runtime-state file,
// never trusted from a cached "running" flag anywhere on disk.
// ----------------------------------------------------------------------------

type ProjectStatus struct {
	Workspace   string
	Container   string
	Session     string
	ContainerUp bool
	TmuxUp      bool
	MitmUp      bool
	MitmWebPort int
}

func projectLiveStatus(workspace string) ProjectStatus {
	status := ProjectStatus{
		Workspace: workspace,
		Container: containerNameForWorkspace(workspace),
		Session:   tmuxSessionNameForWorkspace(workspace),
	}

	status.ContainerUp = dockerContainerRunning(status.Container)
	status.TmuxUp = tmuxSessionExists(status.Session)

	if state, err := readProxyRuntimeStateFor(workspace); err != nil {
		debugf("Warning: failed to read proxy runtime state for %s: %v", workspace, err)
	} else if state != nil && pidAlive(state.Pid) {
		status.MitmUp = true
		status.MitmWebPort = state.WebPort
	}

	return status
}

// ----------------------------------------------------------------------------
// Dashboard HTTP server
// ----------------------------------------------------------------------------

//go:embed webui/templates/*.tmpl webui/static/*
var webuiFS embed.FS

var dashboardTemplates = template.Must(template.ParseFS(webuiFS, "webui/templates/*.tmpl"))

const dashboardCookieName = "sc_dash_token"

// projectID derives a short, stable, URL-safe id for a workspace path, so URLs
// don't need to embed (and percent-encode) an absolute filesystem path.
func projectID(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(sum[:])[:12]
}

func lookupWorkspaceByID(id string) (string, error) {
	reg, err := readRegistry()
	if err != nil {
		return "", err
	}
	for _, p := range reg.Projects {
		if projectID(p.Workspace) == id {
			return p.Workspace, nil
		}
	}
	return "", fmt.Errorf("unknown project id: %s", id)
}

type dashboardServer struct {
	mu    sync.Mutex
	terms map[*termSession]struct{} // live browser-terminal PTYs (see terminal.go)
	token string
}

func newDashboardServer(token string) *dashboardServer {
	return &dashboardServer{terms: make(map[*termSession]struct{}), token: token}
}

// requireAuth gates every route but /healthz behind a random per-launch token.
// Loopback-only binding (see cmdDashboardServe) keeps the dashboard off the
// network entirely, but that alone doesn't stop a malicious page open in
// another browser tab from targeting 127.0.0.1 (DNS-rebinding-style attacks) —
// worth defending against here since the terminal tab grants a real shell.
// A valid ?token= is accepted once, then remembered via an HttpOnly cookie so
// reloading/reopening the page doesn't require re-pasting it.
func (d *dashboardServer) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(dashboardCookieName); err == nil {
			if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(d.token)) == 1 {
				next(w, r)
				return
			}
		}

		if tok := r.URL.Query().Get("token"); tok != "" {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(d.token)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name:     dashboardCookieName,
					Value:    d.token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				q := r.URL.Query()
				q.Del("token")
				cleanURL := r.URL.Path
				if enc := q.Encode(); enc != "" {
					cleanURL += "?" + enc
				}
				http.Redirect(w, r, cleanURL, http.StatusFound)
				return
			}
		}

		http.Error(w, "403 Forbidden — missing or invalid dashboard token", http.StatusForbidden)
	}
}

// routes wires up the full route table. Deliberately not using Go 1.22's
// wildcard ServeMux patterns ("/p/{id}") — go.mod targets go 1.21, and those
// patterns are silently treated as literal path segments on older toolchains
// rather than failing to compile, which would be a nasty surprise. Manual
// prefix parsing in handleRoot works identically on any Go version.
func (d *dashboardServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)

	staticFS, err := fs.Sub(webuiFS, "webui/static")
	if err != nil {
		log.Fatalf("dashboard: bad embedded static assets: %v", err)
	}
	mux.Handle("/static/", d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))).ServeHTTP(w, r)
	}))

	mux.HandleFunc("/", d.requireAuth(d.handleRoot))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (d *dashboardServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		d.handleIndex(w, r)
		return
	}

	if !strings.HasPrefix(path, "/p/") {
		http.NotFound(w, r)
		return
	}

	rest := strings.TrimPrefix(path, "/p/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch {
	case sub == "":
		d.handleProject(w, r, id)
	case sub == "terminal/ws":
		d.handleTerminalWS(w, r, id)
	case sub == "terminal" || sub == "terminal/":
		d.handleTerminalPage(w, r, id)
	case sub == "mitm/flows":
		d.handleMitmFlows(w, r, id)
	case sub == "firewall/stream":
		d.handleFirewallStream(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

type projectRow struct {
	ID string
	ProjectStatus
}

func (d *dashboardServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	reg, err := readRegistry()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]projectRow, 0, len(reg.Projects))
	for _, p := range reg.Projects {
		rows = append(rows, projectRow{ID: projectID(p.Workspace), ProjectStatus: projectLiveStatus(p.Workspace)})
	}

	data := struct{ Projects []projectRow }{Projects: rows}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplates.ExecuteTemplate(w, "index.html.tmpl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (d *dashboardServer) handleProject(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	data := struct {
		ID string
		ProjectStatus
	}{ID: id, ProjectStatus: projectLiveStatus(workspace)}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplates.ExecuteTemplate(w, "project.html.tmpl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// shutdown closes every live browser-terminal PTY (see terminal.go). It never
// touches the underlying tmux sessions or Docker containers — those keep running
// independent of the dashboard, by design; closing a terminal only detaches.
func (d *dashboardServer) shutdown() {
	d.shutdownTerminals()
}

// handleMitmFlows returns mitmweb's flow list as JSON so the Mitm tab can render
// a native flow table (see webui/static/mitm.js) instead of embedding mitmweb's
// SPA — which can't be reverse-proxied under a subpath (absolute asset paths, no
// base-path option, DNS-rebinding Host guard).
//
// The single /flows endpoint sidesteps all of that: we fetch it server-side with
// the Host header set to the loopback IP mitmweb demands, so the browser never
// talks to mitmweb directly and never trips its rebinding check. Only the flow
// list is exposed — none of mitmweb's mutating endpoints (/clear, /flows/kill,
// /flows/resume) are proxied.
func (d *dashboardServer) handleMitmFlows(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	state, err := readProxyRuntimeStateFor(workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if state == nil || !pidAlive(state.Pid) {
		http.Error(w, "credential proxy is not running for this project", http.StatusBadGateway)
		return
	}

	upstream := fmt.Sprintf("http://127.0.0.1:%d/flows", state.WebPort)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// mitmweb rejects any Host header that isn't a bare loopback IP (its
	// DNS-rebinding guard). Setting it here is the whole reason this must be a
	// server-side fetch rather than a browser-side reverse proxy.
	req.Host = fmt.Sprintf("127.0.0.1:%d", state.WebPort)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to reach mitmweb: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

const (
	firewallSeedBytes      = 256 * 1024
	firewallSeedLines      = 500
	firewallPollInterval   = 500 * time.Millisecond
	firewallHeartbeatEvery = 30 // ticks (~15s at the poll interval above)
)

// readTailLines returns up to maxLines from the end of path, reading at most the
// last maxBytes of the file (avoids loading a large, long-running log fully into
// memory just to get its tail). Also returns the file's current size, so the
// caller can start polling for new bytes from that offset.
func readTailLines(path string, maxBytes int64, maxLines int) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := stat.Size()

	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // first line after a mid-file seek is likely partial
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, size, nil
}

// sseEscape strips trailing \r so a Windows-style log line doesn't corrupt the
// SSE "data:" field (lines are already split on \n by the caller).
func sseEscape(line string) string {
	return strings.TrimRight(line, "\r")
}

func writeSSELine(w http.ResponseWriter, line string) {
	fmt.Fprintf(w, "data: %s\n\n", sseEscape(line))
}

// handleFirewallStream tails <workspace>/.sandclaude/logs/proxy.log directly off
// the host filesystem — no docker exec needed, since that file is already
// bind-mounted read-write into the container (see startProxy/startDocker) and so
// persists independent of whether the container is currently running. This is
// why the firewall tab keeps showing history even after a project's container
// is stopped or removed.
func (d *dashboardServer) handleFirewallStream(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	logPath := filepath.Join(logsDirForWorkspace(workspace), "proxy.log")

	lines, offset, err := readTailLines(logPath, firewallSeedBytes, firewallSeedLines)
	if err != nil {
		// Missing/unreadable file (e.g. this project has never been started) —
		// send one error event and end the stream. EventSource auto-reconnects
		// after a short delay, so it recovers on its own once the file appears.
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", sseEscape(err.Error()))
		flusher.Flush()
		return
	}
	for _, line := range lines {
		writeSSELine(w, line)
	}
	flusher.Flush()

	ticker := time.NewTicker(firewallPollInterval)
	defer ticker.Stop()

	ticks := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			ticks++
			wrote := d.pollFirewallLog(w, logPath, &offset)
			if wrote {
				flusher.Flush()
			} else if ticks%firewallHeartbeatEvery == 0 {
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}
}

// pollFirewallLog reads any bytes appended since *offset and writes them as SSE
// lines, advancing *offset. Resets to 0 if the file shrank (log rotation/
// truncation) so a restart doesn't leave the offset stuck past EOF.
func (d *dashboardServer) pollFirewallLog(w http.ResponseWriter, logPath string, offset *int64) bool {
	f, err := os.Open(logPath)
	if err != nil {
		return false // transient — file may not exist yet if the project hasn't started
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return false
	}
	size := stat.Size()
	if size < *offset {
		*offset = 0
	}
	if size <= *offset {
		return false
	}

	if _, err := f.Seek(*offset, io.SeekStart); err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	*offset = size

	wrote := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		writeSSELine(w, line)
		wrote = true
	}
	return wrote
}

// ----------------------------------------------------------------------------
// CLI: `sandclaude dashboard` / `sandclaude dashboard stop` / the internal
// `sandclaude dashboard-serve` the former re-execs to actually run the server.
//
// The dashboard is a singleton, host-wide, long-lived daemon (unlike every
// other sandclaude command, which is scoped to the current project and exits
// when its work is done) — state in ~/.sandclaude/dashboard.json tracks the
// one running instance so a second `sandclaude dashboard` just prints the
// existing URL instead of spawning a duplicate.
// ----------------------------------------------------------------------------

type dashboardState struct {
	Pid       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	StartedAt string `json:"started_at"`
}

func dashboardStatePath() string {
	return filepath.Join(sandclaudeHome(), "dashboard.json")
}

func readDashboardState() (*dashboardState, error) {
	data, err := os.ReadFile(dashboardStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state dashboardState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeDashboardState(state *dashboardState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dashboardStatePath(), data, 0600)
}

func removeDashboardState() {
	os.Remove(dashboardStatePath())
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func dashboardHealthy(port int) bool {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func printDashboardURL(state *dashboardState) {
	fmt.Printf("Dashboard running at http://127.0.0.1:%d/?token=%s\n", state.Port, state.Token)
	fmt.Println("(the token is remembered as a cookie after your first visit — you won't need to paste it again)")
}

func cmdDashboard(args []string) error {
	if len(args) > 0 && args[0] == "stop" {
		return cmdDashboardStop()
	}
	return cmdDashboardStart()
}

// cmdDashboardStart re-execs the sandclaude binary as `dashboard-serve`,
// detached via Setsid so it keeps running after this command's process exits —
// more robust than dev-mode mitmweb's current implicit-reparenting persistence
// (main.go Run(), sc.detachedSession != ""), which this daemon is deliberately
// not modeled after since it's long-lived by design rather than incidentally.
func cmdDashboardStart() error {
	state, err := ensureDashboardRunning()
	if err != nil {
		return err
	}
	printDashboardURL(state)
	return nil
}

// ensureDashboardRunning returns the state of the already-running dashboard,
// or spawns it as a detached daemon (same re-exec approach as cmdDashboardStart
// used to do directly) if it isn't running yet. Shared by `sandclaude dashboard`
// and `sandclaude start`/`dev`, which both want the singleton daemon up without
// caring which of them happened to be the one that launched it.
func ensureDashboardRunning() (*dashboardState, error) {
	if state, err := readDashboardState(); err == nil && state != nil && pidAlive(state.Pid) && dashboardHealthy(state.Port) {
		return state, nil
	}

	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate dashboard token: %w", err)
	}

	port, err := findFreePort(7777)
	if err != nil {
		return nil, fmt.Errorf("failed to find free port for dashboard: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve sandclaude binary path: %w", err)
	}

	if err := os.MkdirAll(sandclaudeHome(), 0700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(sandclaudeHome(), "dashboard.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exePath, "dashboard-serve", "--port", fmt.Sprintf("%d", port), "--token", token)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start dashboard server: %w", err)
	}

	state := &dashboardState{
		Pid:       cmd.Process.Pid,
		Port:      port,
		Token:     token,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeDashboardState(state); err != nil {
		return nil, err
	}

	time.Sleep(500 * time.Millisecond) // give it a moment to bind before use
	return state, nil
}

func cmdDashboardStop() error {
	state, err := readDashboardState()
	if err != nil {
		return err
	}
	if state == nil || !pidAlive(state.Pid) {
		fmt.Println("Dashboard is not running.")
		removeDashboardState()
		return nil
	}

	if err := syscall.Kill(state.Pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to stop dashboard (pid %d): %w", state.Pid, err)
	}
	removeDashboardState()
	fmt.Println("Dashboard stopped.")
	return nil
}

// cmdDashboardServe is the actual long-running server process, spawned only by
// cmdDashboardStart — intentionally undocumented in usage(), like startDetached
// isn't itself a user-facing command.
func cmdDashboardServe(args []string) error {
	fset := flag.NewFlagSet("dashboard-serve", flag.ContinueOnError)
	port := fset.Int("port", 0, "port to listen on")
	token := fset.String("token", "", "auth token")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *port == 0 || *token == "" {
		return fmt.Errorf("dashboard-serve requires --port and --token")
	}

	server := newDashboardServer(*token)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", *port),
		Handler: server.routes(),
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Dashboard received shutdown signal, cleaning up...")
		server.shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
	}()

	log.Printf("sandclaude dashboard listening on http://127.0.0.1:%d", *port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
