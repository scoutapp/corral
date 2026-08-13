package dashboard

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
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/session"
	"github.com/scoutapp/corral/internal/store"
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
// Project registry (~/.corral/projects.json)
//
// Purely a discovery aid — "which workspace paths has the user ever started a
// project in" — never trusted for liveness. Whether a registered project is
// actually running right now is always re-derived on demand (projectLiveStatus),
// the same way session.RunningContainerName() already re-checks Docker rather than
// caching a status flag. This sidesteps the many unreliable ways a project can
// stop (Ctrl-C, `docker kill`, crash, `corral remove`) — there is no single
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
	return filepath.Join(config.CorralHome(), "projects.json")
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
	if err := os.MkdirAll(config.CorralHome(), 0700); err != nil {
		return err
	}
	return os.WriteFile(registryPath(), data, 0600)
}

// RegisterProject upserts a workspace's entry (matched by absolute path) with the
// current timestamp. Called from Run() every time a project starts.
func RegisterProject(workspace string) error {
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
// Every existing helper (config.CorralDir/config.GetProjectDir/config.GetLogsDir) resolves via
// os.Getwd(), which is correct for every existing command — they only ever
// operate on "the project you're standing in." The dashboard is host-wide and
// needs to inspect *other* projects regardless of its own cwd, so it needs
// workspace-parameterized siblings. These assume what the rest of the codebase
// already assumes (README: `corral init` is run from the project's own
// directory) — that .corral/ lives directly under the workspace path.
// ----------------------------------------------------------------------------

func projectDirForWorkspace(workspace string) string {
	return filepath.Join(workspace, ".corral", "project")
}

func logsDirForWorkspace(workspace string) string {
	return filepath.Join(workspace, ".corral", "logs")
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

// ProxyRuntimeStatePathFor returns the runtime-state file path for a given
// workspace. Both the write path (WriteProxyRuntimeState) and the read path
// (readProxyRuntimeStateFor) derive from projectDirForWorkspace(workspace) so
// they are guaranteed symmetric even when the writing process's cwd differs
// from the workspace (e.g. `corral start` run from another directory).
func ProxyRuntimeStatePathFor(workspace string) string {
	return filepath.Join(projectDirForWorkspace(workspace), "runtime.json")
}

// WriteProxyRuntimeState is called from startProxy() with the project's
// workspace path. It writes to the workspace's project dir (NOT cwd) so the
// dashboard, which reads the same workspace-derived path, always sees fresh
// state regardless of the writer's working directory.
func WriteProxyRuntimeState(workspace string, webPort int, pid int) error {
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
	if err := os.MkdirAll(projectDirForWorkspace(workspace), 0700); err != nil {
		return err
	}
	return os.WriteFile(ProxyRuntimeStatePathFor(workspace), data, 0600)
}

// RemoveProxyRuntimeState deletes a workspace's runtime-state file. Used by
// stopProxy so cleanup targets the same workspace-derived path that was
// written, not a cwd-relative one. A missing file is not an error.
func RemoveProxyRuntimeState(workspace string) error {
	if err := os.Remove(ProxyRuntimeStatePathFor(workspace)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// readProxyRuntimeStateFor reads another project's runtime state by workspace path.
// Returns (nil, nil) if the file doesn't exist (proxy never started, or already
// cleaned up) rather than an error — absence is a normal, expected state here.
func readProxyRuntimeStateFor(workspace string) (*ProxyRuntimeState, error) {
	path := ProxyRuntimeStatePathFor(workspace)
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

// mitmwebResponding is a lightweight liveness probe for a recorded mitmweb web
// port: it GETs mitmweb's /flows API with a short timeout and returns true only
// on a 2xx response. Used as a safety net alongside session.PidAlive so a
// stale/reused port (e.g. an old runtime.json whose pid happens to be alive
// again, or a port now owned by an unrelated process) is treated as "not
// running." mitmweb rejects any Host header that isn't a bare loopback IP (its
// DNS-rebinding guard), so req.Host must be set explicitly here too.
func mitmwebResponding(webPort int) bool {
	upstream := fmt.Sprintf("http://127.0.0.1:%d/flows", webPort)
	req, err := http.NewRequest(http.MethodGet, upstream, nil)
	if err != nil {
		return false
	}
	req.Host = fmt.Sprintf("127.0.0.1:%d", webPort)
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
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

	// Activity is a coarse "what is this project doing" signal derived from the
	// rate of api.anthropic.com requests in proxy.log — see activity.go.
	Activity      string // "working" | "waiting" | "off"
	AnthropicHits int    // hits in the recent window, for display
}

func projectLiveStatus(workspace string) ProjectStatus {
	status := ProjectStatus{
		Workspace: workspace,
		Container: session.ContainerNameForWorkspace(workspace),
		Session:   session.TmuxSessionNameForWorkspace(workspace),
	}

	status.ContainerUp = session.DockerContainerRunning(status.Container)
	// Live (not dead-pane) so the dashboard doesn't connect the Claude terminal to
	// a session whose container has exited — attaching to a dead pane shows tmux's
	// blank "Pane is dead" fill instead of a terminal.
	status.TmuxUp = session.TmuxSessionLive(status.Session)

	if state, err := readProxyRuntimeStateFor(workspace); err != nil {
		config.Debugf("Warning: failed to read proxy runtime state for %s: %v", workspace, err)
	} else if state != nil && session.PidAlive(state.Pid) {
		status.MitmUp = true
		status.MitmWebPort = state.WebPort
	}

	status.Activity, status.AnthropicHits = projectActivity(workspace, status.ContainerUp, status.TmuxUp)

	return status
}

// ----------------------------------------------------------------------------
// Dashboard HTTP server
// ----------------------------------------------------------------------------

// all:webui/static recurses into the built React app under static/app/
// (static/app/index.html + static/app/assets/*), which a bare webui/static/*
// glob would not reach. The whole UI is now the React SPA served from
// static/app; there are no server-rendered templates anymore.
//
//go:embed all:webui/static
var webuiFS embed.FS

const dashboardCookieName = "corral_dash_token"

// ProjectID derives a short, stable, URL-safe id for a workspace path, so URLs
// don't need to embed (and percent-encode) an absolute filesystem path.
func ProjectID(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(sum[:])[:12]
}

func lookupWorkspaceByID(id string) (string, error) {
	reg, err := readRegistry()
	if err != nil {
		return "", err
	}
	for _, p := range reg.Projects {
		if ProjectID(p.Workspace) == id {
			return p.Workspace, nil
		}
	}
	return "", fmt.Errorf("unknown project id: %s", id)
}

type dashboardServer struct {
	mu    sync.Mutex
	terms map[*termSession]struct{} // live browser-terminal PTYs (see terminal.go)
	token string
	// bootID is a fresh random value each time the dashboard daemon starts. It is
	// surfaced in /status so the browser can tell when the server has restarted
	// and drop stale per-project UI state (e.g. mute prefs keyed by project id).
	bootID string
	// store is the shared Corral database (~/.corral/corral.db). It backs the PR
	// Review feature and is opened lazily on first use so the dashboard still
	// starts if the DB can't be opened; PR-review endpoints then report the error.
	store *store.Store
}

func newDashboardServer(token string) *dashboardServer {
	boot, _ := randomToken()
	return &dashboardServer{terms: make(map[*termSession]struct{}), token: token, bootID: boot}
}

// getStore opens the shared Corral database on first use and caches the handle.
// Opening runs migrations, so it is deferred out of newDashboardServer to keep
// dashboard startup independent of DB health.
func (d *dashboardServer) getStore() (*store.Store, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.store != nil {
		return d.store, nil
	}
	s, err := store.Open()
	if err != nil {
		return nil, err
	}
	d.store = s
	return s, nil
}

// requireAuth gates every route but /healthz behind a random per-launch token.
// Loopback-only binding (see CmdDashboardServe) keeps the dashboard off the
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

// serveSPA writes the built React app's index.html (from the embedded
// static/app bundle) for a client-routed page. The SPA's own History-API router
// then renders the right view for "/", "/global", or "/p/<id>". Hashed JS/CSS
// under /static/app/assets are served by the /static file server. If the bundle
// isn't built yet (fresh checkout before `npm run build`), we say so clearly.
func (d *dashboardServer) serveSPA(w http.ResponseWriter, _ *http.Request) {
	html, err := webuiFS.ReadFile("webui/static/app/index.html")
	if err != nil {
		http.Error(w, "dashboard UI bundle not built — run `npm run build` in internal/dashboard/webui/app-src (or reinstall via install.sh)", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// NEVER cache the shell: it references content-hashed asset filenames, so after
	// a redeploy (install + dashboard restart) a plain browser reload must pick up
	// the NEW index.html to load the new bundle. A cached shell points at stale
	// assets — the recurring "I reinstalled but still see the old UI" problem. The
	// hashed /static/app/assets/* files are immutable and cache fine on their own.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Write(html)
}

func (d *dashboardServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		d.serveSPA(w, r)
		return
	}

	// Live status JSON for the landing page to poll (activity, up/down per project)
	// without re-rendering the whole index HTML.
	if path == "/status" {
		d.handleStatus(w, r)
		return
	}

	// Update availability for the global banner, and the host-PTY that runs
	// `corral update` behind the "Update" button.
	if path == "/update-status" {
		d.handleUpdateStatus(w, r)
		return
	}
	if path == "/update/ws" {
		d.handleUpdateWS(w, r)
		return
	}

	// Global (cross-project) control plane.
	switch path {
	case "/global":
		d.serveSPA(w, r)
		return
	case "/global/config":
		d.handleGlobalRead(w, r)
		return
	case "/global/apply":
		d.handleGlobalApply(w, r)
		return
	case "/global/populate":
		d.handleGlobalPopulate(w, r)
		return
	case "/global/populate/ws":
		d.handleSessionWS(w, r, "corral-populate-creds")
		return
	}

	// Repos list (cross-project). /repos (GET list, POST add) and
	// /repos/<id>[/fetch|/prs|/forensics] (item actions).
	if path == "/repos" {
		d.handleRepos(w, r)
		return
	}
	if strings.HasPrefix(path, "/repos/") {
		rest := strings.TrimPrefix(path, "/repos/")
		// A bare GET /repos/<id> is browser navigation to the repo-hub page
		// (RepoPage) — serve the SPA shell so a hard reload lands correctly.
		// Sub-actions (/repos/<id>/prs, …) and non-GET verbs are API calls.
		if r.Method == http.MethodGet && !strings.Contains(rest, "/") {
			d.serveSPA(w, r)
			return
		}
		d.handleRepoItem(w, r, rest)
		return
	}
	// PR Review per-PR actions (GET /prs/<prId>/blocks). PR-review reads/writes
	// keyed by repo live under /repos/<id>/…; this handles the PR-scoped ones.
	if strings.HasPrefix(path, "/prs/") {
		d.handlePRItem(w, r, strings.TrimPrefix(path, "/prs/"))
		return
	}
	if path == "/projects/create" {
		d.handleCreateProject(w, r)
		return
	}
	if path == "/gh/repos" {
		d.handleGhRepos(w, r)
		return
	}
	if path == "/gh/branches" {
		d.handleGhBranches(w, r)
		return
	}
	if path == "/gh/issues" {
		d.handleGhIssues(w, r)
		return
	}
	if path == "/gh/issues/create" {
		d.handleGhIssueCreate(w, r)
		return
	}
	if path == "/gh/issues/draft" {
		d.handleGhIssueDraftWS(w, r)
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
		// The project page is now the React SPA (client-routed at /p/<id>). Its
		// data comes from the JSON/WS endpoints below, not a server-rendered page.
		d.serveSPA(w, r)
	case sub == "terminal/ws":
		d.handleTerminalWS(w, r, id)
	case sub == "terminal/action":
		d.handleTerminalAction(w, r, id)
	case sub == "config":
		d.handleConfigRead(w, r, id)
	case sub == "config/diff":
		d.handleConfigDiff(w, r, id)
	case sub == "config/apply":
		d.handleConfigApply(w, r, id)
	case sub == "config/restart":
		d.handleConfigRestart(w, r, id)
	case sub == "remove":
		d.handleRemoveProject(w, r, id)
	case sub == "files/tree":
		d.handleFilesTree(w, r, id)
	case sub == "files/read":
		d.handleFilesRead(w, r, id)
	case sub == "files/write":
		d.handleFilesWrite(w, r, id)
	case sub == "files/new":
		d.handleFilesNew(w, r, id)
	case sub == "files/mkdir":
		d.handleFilesMkdir(w, r, id)
	case sub == "files/rename":
		d.handleFilesRename(w, r, id)
	case sub == "files" || sub == "files/":
		d.handleFilesDelete(w, r, id)
	case sub == "files/find":
		d.handleFilesFind(w, r, id)
	case sub == "files/grep":
		d.handleFilesGrep(w, r, id)
	case sub == "git/status":
		d.handleGitStatus(w, r, id)
	case sub == "git/diff":
		d.handleGitDiff(w, r, id)
	case sub == "git/file":
		d.handleGitFile(w, r, id)
	case sub == "git/refs":
		d.handleGitRefs(w, r, id)
	case sub == "git/repos":
		d.handleGitRepos(w, r, id)
	case sub == "container/ws":
		d.handleContainerWS(w, r, id)
	case sub == "host/ws":
		d.handleHostWS(w, r, id)
	case sub == "start":
		d.handleStartProject(w, r, id)
	case sub == "stop":
		d.handleStopProject(w, r, id)
	case sub == "populate-prompt":
		d.handlePopulatePrompt(w, r, id)
	case sub == "sshkeys/available":
		d.handleSSHKeysAvailable(w, r, id)
	case sub == "sshkeys/status":
		d.handleSSHKeysStatus(w, r, id)
	case sub == "sshkeys/select":
		d.handleSSHKeysSelect(w, r, id)
	case sub == "sshkeys/ws":
		d.handleSSHKeysLoadWS(w, r, id)
	case sub == "chat/ws":
		d.handleChatWS(w, r, id)
	case sub == "mitm/flows":
		d.handleMitmFlows(w, r, id)
	case sub == "mitm/direct":
		d.handleMitmDirect(w, r, id)
	case strings.HasPrefix(sub, "mitm/flows/"):
		d.handleMitmContent(w, r, id, strings.TrimPrefix(sub, "mitm/flows/"))
	case sub == "firewall/stream":
		d.handleFirewallStream(w, r, id)
	default:
		http.NotFound(w, r)
	}
}


// statusRow is the per-project JSON the landing page polls for live pane updates.
type statusRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Workspace     string `json:"workspace"`
	ContainerUp   bool   `json:"container_up"`
	TmuxUp        bool   `json:"tmux_up"`
	MitmUp        bool   `json:"mitm_up"`
	Activity      string `json:"activity"` // working | waiting | off
	AnthropicHits int    `json:"anthropic_hits"`
	Peek          string `json:"peek"` // last non-empty terminal line, for the pane preview
}

// handleStatus returns live status for every registered project as JSON. The
// landing page polls this to keep the panes fresh without a full HTML re-render.
func (d *dashboardServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	reg, err := readRegistry()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Compute each project's live status CONCURRENTLY. projectLiveStatus shells
	// out to docker + tmux and reads a log — ~250-300ms each — so doing N projects
	// serially made /status take seconds (e.g. ~2.4s for 7 projects), and it's
	// polled every few seconds. Fan out one goroutine per project; results go into
	// a pre-sized slice by index so order is preserved without a mutex.
	rows := make([]statusRow, len(reg.Projects))
	var wg sync.WaitGroup
	for i, p := range reg.Projects {
		wg.Add(1)
		go func(i int, workspace string) {
			defer wg.Done()
			st := projectLiveStatus(workspace)
			row := statusRow{
				ID:            ProjectID(workspace),
				Name:          filepath.Base(workspace),
				Workspace:     workspace,
				ContainerUp:   st.ContainerUp,
				TmuxUp:        st.TmuxUp,
				MitmUp:        st.MitmUp,
				Activity:      st.Activity,
				AnthropicHits: st.AnthropicHits,
			}
			if st.TmuxUp {
				row.Peek = tmuxLastLine(st.Session)
			}
			rows[i] = row
		}(i, p.Workspace)
	}
	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"projects": rows, "boot_id": d.bootID})
}

// handleRemoveProject unregisters a project from the dashboard registry
// (projects.json) by its ProjectID. It does NOT touch the project's on-disk
// ./.corral/ (config, allowlist, logs) — it only removes it from the
// dashboard list; `corral start` re-registers it. POST only.
func (d *dashboardServer) handleRemoveProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reg, err := readRegistry()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kept := reg.Projects[:0]
	found := false
	for _, p := range reg.Projects {
		if ProjectID(p.Workspace) == id {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	reg.Projects = kept
	if err := writeRegistry(reg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// tmuxLastLine returns the last meaningful line of a tmux session's current
// screen — a cheap "what's on screen right now" peek for the project pane.
// Capturing without -S/-E and dropping the very last row skips tmux's own status
// bar (the "[session] ... time date" line), which is never useful content. Best
// effort: returns "" if capture fails.
func tmuxLastLine(session string) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", session, "-p").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s == "" {
			continue
		}
		// Skip tmux's status bar and Claude Code's box-drawing chrome, which read
		// as noise in a one-line preview.
		if strings.Contains(s, "\"✳") || strings.HasPrefix(s, "[") && strings.Contains(s, ":claude") {
			continue
		}
		if strings.Trim(s, "─╭╮╰╯│ ") == "" {
			continue
		}
		return s
	}
	return ""
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
	if state == nil || !session.PidAlive(state.Pid) || !mitmwebResponding(state.WebPort) {
		http.Error(w, "credential proxy is not running for this project", http.StatusBadGateway)
		return
	}

	d.proxyMitmGet(w, r, state.WebPort, "/flows", "application/json")
}

// handleMitmContent proxies a single flow's request/response body from mitmweb.
// tail is the part after "mitm/flows/" — expected form "<flowID>/<side>/content"
// where side is "request" or "response". mitmweb serves the raw body at
// /flows/<id>/<side>/content.data; the content-type is whatever the captured
// traffic was, so it's passed through from mitmweb rather than forced.
func (d *dashboardServer) handleMitmContent(w http.ResponseWriter, r *http.Request, id, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) != 3 || parts[2] != "content" || (parts[1] != "request" && parts[1] != "response") {
		http.NotFound(w, r)
		return
	}
	flowID, side := parts[0], parts[1]
	// flowIDs are mitmweb UUIDs; reject anything with path characters so this
	// can't be coaxed into fetching an arbitrary mitmweb path.
	if flowID == "" || strings.ContainsAny(flowID, "/.\\") {
		http.NotFound(w, r)
		return
	}

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
	if state == nil || !session.PidAlive(state.Pid) || !mitmwebResponding(state.WebPort) {
		http.Error(w, "credential proxy is not running for this project", http.StatusBadGateway)
		return
	}

	path := fmt.Sprintf("/flows/%s/%s/content.data", flowID, side)
	d.proxyMitmGet(w, r, state.WebPort, path, "")
}

// directHost is ONE allowed-but-not-decrypted request surfaced from the proxy
// log — one entry per DIRECT line (not deduped), so the Mitm tab can interleave
// each individual direct-dialed request chronologically with the decrypted flows.
type directHost struct {
	Host string `json:"host"` // hostname:port
	When string `json:"when"` // "YYYY/MM/DD HH:MM:SS" (proxy log stamp, local)
	TS   int64  `json:"ts"`   // Unix seconds parsed from When (0 if unparseable), for sorting
}

// mitmDirectRecentCap bounds the no-filter response (the 2s poll). mitmDirectQueryCap
// bounds a ?q= full-log search so a giant log can't produce an unbounded payload.
const (
	mitmDirectRecentCap = 500
	mitmDirectQueryCap  = 1000
)

// parseProxyLogStamp parses the proxy log's "2006/01/02 15:04:05" stamp into
// Unix seconds; returns 0 if it doesn't parse. The stamp is UTC: the
// allowlist-proxy runs inside the container (which is UTC) and logs with
// log.LUTC, so the wall-clock is UTC regardless of the dashboard host's zone.
// Parsing as time.Local (the host zone) skewed direct rows by the host's UTC
// offset relative to the mitmweb flows (whose timestamps are epoch already),
// misordering the interleaved Mitm table.
func parseProxyLogStamp(s string) int64 {
	t, err := time.ParseInLocation("2006/01/02 15:04:05", strings.TrimSpace(s), time.UTC)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// handleMitmDirect scans this project's proxy.log for DIRECT lines — requests
// that were allowed but direct-dialed (not routed through mitmweb, so never
// decrypted). Each line becomes its own entry so the Mitm tab shows individual
// direct requests inline in the flow, not one grouped row per host.
//
//   - no ?q=  : the most recent mitmDirectRecentCap DIRECT lines (cheap — this
//     is the 2s poll). Only a bounded tail of the log is read.
//   - ?q=<s>  : search the WHOLE log for DIRECT lines whose host contains <s>
//     (case-insensitive), returning the newest mitmDirectQueryCap matches. This
//     is how the Mitm host filter reaches full on-disk history for direct hosts
//     (mitmweb has no record of them — the log is the only source).
//
// Log line shape (see allowlist-proxy/main.go):
//   2026/08/05 00:56:02 DIRECT   http-intake.logs.us5.datadoghq.com:443 (not-monitored)
func (d *dashboardServer) handleMitmDirect(w http.ResponseWriter, r *http.Request, id string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	logPath := filepath.Join(logsDirForWorkspace(workspace), "proxy.log")

	// With a query, read the whole log (bounded output cap keeps it safe); without,
	// only a recent tail — the common poll path stays cheap.
	var lines []string
	var rerr error
	if q != "" {
		lines, rerr = readAllLines(logPath)
	} else {
		lines, _, rerr = readTailLines(logPath, 512*1024, 4000)
	}
	if rerr != nil {
		// No log yet (never started) — empty set, not an error, so the tab still
		// shows the decrypted flows.
		writeJSON(w, map[string]any{"direct": []directHost{}})
		return
	}

	out := make([]directHost, 0, mitmDirectRecentCap)
	for _, ln := range lines {
		i := strings.Index(ln, " DIRECT")
		if i < 0 {
			continue
		}
		stamp := strings.TrimSpace(ln[:i])
		rest := strings.TrimSpace(ln[i+len(" DIRECT"):])
		host := rest
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			host = rest[:sp]
		}
		if host == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(host), q) {
			continue
		}
		out = append(out, directHost{Host: host, When: stamp, TS: parseProxyLogStamp(stamp)})
	}

	// Keep the newest N (lines are chronological, so the tail is newest).
	limit := mitmDirectRecentCap
	if q != "" {
		limit = mitmDirectQueryCap
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	writeJSON(w, map[string]any{"direct": out})
}

// proxyMitmGet performs a server-side GET against mitmweb on webPort and copies
// the response back. It exists because mitmweb rejects any Host header that isn't
// a bare loopback IP (its DNS-rebinding guard) — setting req.Host here is the
// whole reason these must be server-side fetches rather than browser-side
// reverse proxies. If contentType is non-empty it overrides the response type;
// otherwise mitmweb's own content-type is passed through.
func (d *dashboardServer) proxyMitmGet(w http.ResponseWriter, r *http.Request, webPort int, path, contentType string) {
	upstream := fmt.Sprintf("http://127.0.0.1:%d%s", webPort, path)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Host = fmt.Sprintf("127.0.0.1:%d", webPort)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to reach mitmweb: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
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

// readAllLines reads a whole log file into lines, bounded to the last
// readAllCap bytes as a safety valve against a pathologically large log (still
// far more than any realistic proxy.log). Used by the direct-host history
// search, where the caller further caps the matched output.
const readAllCap = 32 * 1024 * 1024

func readAllLines(path string) ([]string, error) {
	lines, _, err := readTailLines(path, readAllCap, 1<<31-1)
	return lines, err
}

// sseEscape strips trailing \r so a Windows-style log line doesn't corrupt the
// SSE "data:" field (lines are already split on \n by the caller).
func sseEscape(line string) string {
	return strings.TrimRight(line, "\r")
}

func writeSSELine(w http.ResponseWriter, line string) {
	fmt.Fprintf(w, "data: %s\n\n", sseEscape(line))
}

// handleFirewallStream tails <workspace>/.corral/logs/proxy.log directly off
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
// CLI: `corral dashboard` / `corral dashboard stop` / the internal
// `corral dashboard-serve` the former re-execs to actually run the server.
//
// The dashboard is a singleton, host-wide, long-lived daemon (unlike every
// other corral command, which is scoped to the current project and exits
// when its work is done) — state in ~/.corral/dashboard.json tracks the
// one running instance so a second `corral dashboard` just prints the
// existing URL instead of spawning a duplicate.
// ----------------------------------------------------------------------------

type DashboardState struct {
	Pid       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	StartedAt string `json:"started_at"`
}

func dashboardStatePath() string {
	return filepath.Join(config.CorralHome(), "dashboard.json")
}

func ReadDashboardState() (*DashboardState, error) {
	data, err := os.ReadFile(dashboardStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state DashboardState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeDashboardState(state *DashboardState) error {
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

func printDashboardURL(state *DashboardState) {
	fmt.Printf("Dashboard running at http://127.0.0.1:%d/?token=%s\n", state.Port, state.Token)
	fmt.Println("(the token is remembered as a cookie after your first visit — you won't need to paste it again)")
}

func CmdDashboard(args []string) error {
	if len(args) > 0 && args[0] == "stop" {
		return cmdDashboardStop()
	}
	return cmdDashboardStart()
}

// cmdDashboardStart re-execs the corral binary as `dashboard-serve`,
// detached via Setsid so it keeps running after this command's process exits —
// more robust than dev-mode mitmweb's current implicit-reparenting persistence
// (main.go Run(), sc.detachedSession != ""), which this daemon is deliberately
// not modeled after since it's long-lived by design rather than incidentally.
func cmdDashboardStart() error {
	state, _, err := EnsureDashboardRunning()
	if err != nil {
		return err
	}
	printDashboardURL(state)
	return nil
}

// EnsureDashboardRunning returns the state of the already-running dashboard,
// or spawns it as a detached daemon (same re-exec approach as cmdDashboardStart
// used to do directly) if it isn't running yet. Shared by `corral dashboard`
// and `corral start`/`dev`, which both want the singleton daemon up without
// caring which of them happened to be the one that launched it.
// The bool return reports whether this call spawned a new daemon (true) vs found
// one already running (false) — callers use it to open a browser tab only on the
// first launch, so N project starts don't pop N tabs at the same dashboard.
func EnsureDashboardRunning() (*DashboardState, bool, error) {
	if state, err := ReadDashboardState(); err == nil && state != nil && session.PidAlive(state.Pid) && dashboardHealthy(state.Port) {
		return state, false, nil
	}

	token, err := randomToken()
	if err != nil {
		return nil, false, fmt.Errorf("failed to generate dashboard token: %w", err)
	}

	port, err := config.FindFreePort(7777)
	if err != nil {
		return nil, false, fmt.Errorf("failed to find free port for dashboard: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return nil, false, fmt.Errorf("failed to resolve corral binary path: %w", err)
	}

	if err := os.MkdirAll(config.CorralHome(), 0700); err != nil {
		return nil, false, err
	}
	logPath := filepath.Join(config.CorralHome(), "dashboard.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, false, fmt.Errorf("failed to open %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exePath, "dashboard-serve", "--port", fmt.Sprintf("%d", port), "--token", token)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Resolve `claude` here — this launcher runs in the user's interactive
	// terminal environment (their real PATH, incl. nvm/asdf/etc.), whereas the
	// detached daemon inherits a stripped PATH that often omits it. Pass the
	// absolute path to the daemon so the chat panel can spawn it. (The daemon
	// falls back to its own PATH lookup if this is unset.)
	cmd.Env = os.Environ()
	if claudeBin, err := exec.LookPath("claude"); err == nil {
		cmd.Env = append(cmd.Env, "CORRAL_CLAUDE_BIN="+claudeBin)
	}
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("failed to start dashboard server: %w", err)
	}

	state := &DashboardState{
		Pid:       cmd.Process.Pid,
		Port:      port,
		Token:     token,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeDashboardState(state); err != nil {
		return nil, false, err
	}

	time.Sleep(500 * time.Millisecond) // give it a moment to bind before use
	return state, true, nil
}

func cmdDashboardStop() error {
	state, err := ReadDashboardState()
	if err != nil {
		return err
	}
	if state == nil || !session.PidAlive(state.Pid) {
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

// CmdDashboardServe is the actual long-running server process, spawned only by
// cmdDashboardStart — intentionally undocumented in usage(), like startDetached
// isn't itself a user-facing command.
func CmdDashboardServe(args []string) error {
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

	log.Printf("corral dashboard listening on http://127.0.0.1:%d", *port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
