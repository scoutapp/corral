package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
	"github.com/scoutapp/corral/internal/session"
	sshagent "github.com/scoutapp/corral/internal/ssh"
)

// handleStartProject cold-starts a project's container from the dashboard.
//
//	POST /p/<id>/start
//
// Today the dashboard only ever RESTARTED a project (handleConfigRestart);
// starting a freshly-created one is new. We shell out to this same corral
// binary (`corral dev`, detached, in the workspace) so there is ONE start
// path shared with the CLI, rather than re-implementing container orchestration.
//
// The child is launched THROUGH the operator's login shell so it inherits the
// real interactive PATH (docker/git/claude/tmux live in version-manager dirs the
// detached daemon's stripped PATH usually omits — the same problem the chat panel
// hit). Same host-side trust basis as the host shell.
func (d *dashboardServer) handleStartProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Already running? Report success idempotently.
	if session.DockerContainerRunning(session.ContainerNameForWorkspace(workspace)) {
		writeFilesJSON(w, map[string]any{"ok": true, "already": true})
		return
	}

	// tmux is a hard host dependency (the interactive container runs inside a host
	// tmux session). Fail with a clear message instead of a cryptic downstream one.
	if err := session.RequireTmux(); err != nil {
		w.WriteHeader(http.StatusPreconditionFailed)
		writeFilesJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}

	// Pre-load gate: the child `corral dev` runs detached (no TTY), so if this
	// project has ssh keys configured but they aren't loaded into the scoped agent
	// yet, the child would fail fast. Surface that here so the caller can send the
	// user to the Config tab's "Load keys" flow first (design: pre-load, then start).
	if keys := resolveProjectSSHKeys(workspace); len(keys) > 0 {
		// Coverage check: confirm EVERY resolved key is loaded, not just that the
		// agent holds ≥1 identity — otherwise a project key added on top of the
		// (silently-keychain-loaded) global key never gets prompted for.
		ag, aerr := sshagent.Ensure(ProjectID(workspace), keys)
		if aerr != nil || ag == nil {
			http.Error(w, "ssh agent unavailable", http.StatusInternalServerError)
			return
		}
		// Before demanding an interactive passphrase, try the macOS Keychain: if
		// the passphrase was stored on a prior load, this loads the keys silently
		// (no re-typing) — the whole point of "ask once, reuse". No-op on Linux.
		if !ag.AllKeysLoaded() {
			ag.TryLoadFromKeychain()
		}
		if !ag.AllKeysLoaded() {
			w.WriteHeader(http.StatusConflict)
			writeFilesJSON(w, map[string]any{
				"ok":               false,
				"ssh_keys_pending": true,
				"message":          "ssh keys need loading first — use Config → SSH keys → Load keys, then start",
			})
			return
		}
	}

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// `<shell> -lc 'exec "$0" dev' <corral-abs-path>`: login shell for the full
	// PATH, exec our own binary by absolute path (no lookup needed), args passed
	// positionally so nothing is interpolated into the shell string.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell, "-lc", `exec "$0" dev`, exe)
	cmd.Dir = workspace
	// The user triggered this FROM the dashboard, so they're already in the
	// browser. `corral dev` normally pops a browser window to the project (its
	// browser-first behavior for the CLI). Suppress that here — otherwise starting
	// a project from the UI opens a redundant second window that just flashes and
	// is discarded. CORRAL_NO_BROWSER is honored by config.OpenBrowser.
	cmd.Env = append(os.Environ(), "CORRAL_NO_BROWSER=1")
	if err := cmd.Start(); err != nil {
		http.Error(w, "start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Detach: we don't wait. The dashboard's status poll will show it come up.
	go func() { _ = cmd.Wait() }()

	// Auto-deliver the first-turn task prompt if one is pending. This is what makes
	// a HEADLESSLY-created project (the conductor via the API — no browser to call
	// POST /populate-prompt) actually start working instead of sitting at an idle
	// Claude TUI (the conv-160 bug). claimPendingPrompt atomically reads-and-clears
	// so delivery is fire-once even against the browser's concurrent /populate-prompt.
	if prompt := d.claimPendingPrompt(workspace); prompt != "" {
		go deliverPromptToClaude(workspace, prompt, true /* submit */)
	}

	// Record project.start as a span so the project.start hooks it fires nest
	// under it in the trace. The launch itself is detached (we don't wait), so the
	// span times just the synchronous start + hook dispatch.
	ctx, endSpan := d.applog().StartSpan(r.Context(), applog.Entry{
		Category: applog.CatProject, Event: "project.start",
		Message:   applog.Fmt("Started %s", filepath.Base(workspace)),
		ProjectID: ProjectID(workspace),
	})
	// Built-in start succeeded — fire any project.start hooks (best-effort).
	d.fireProjectStartHooks(ctx, workspace)
	endSpan(nil)
	writeFilesJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("starting %s", session.ContainerNameForWorkspace(workspace))})
}

// handleStopProject stops a running project's container from the dashboard.
//
//	POST /p/<id>/stop
//
// Tears down the container and its detached tmux session (the inverse of start).
// `docker rm -f` (not `kill`) stops AND removes synchronously so a subsequent
// start doesn't race the async --rm cleanup on the container name; kill-session
// clears the tmux session so a later start's `new-session` doesn't collide with a
// dead pane. Idempotent — a project that's already down reports success. The
// scoped ssh-agent is left as-is (a later start re-adopts or re-prompts).
func (d *dashboardServer) handleStopProject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	container := session.ContainerNameForWorkspace(workspace)
	tmuxSession := session.TmuxSessionNameForWorkspace(workspace)

	if !session.DockerContainerRunning(container) {
		// Still clear any lingering dead-pane session so the pane goes fully idle.
		_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()
		writeFilesJSON(w, map[string]any{"ok": true, "already": true})
		return
	}

	// Auto-save the repo baseline on clean shutdown, if this repo has none yet and
	// the project actually built an app image. Detect NOW (container still up so we
	// can read the inner images); the snapshot copies the per-workspace VOLUME,
	// which survives the container, so we run the (slow, multi-GB) copy in the
	// background AFTER stopping — the stop response returns immediately.
	detectCtx, cancelDetect := context.WithTimeout(r.Context(), 15*time.Second)
	repoCache, srcVol := d.repoBaselineAutoSaveTarget(detectCtx, workspace)
	cancelDetect()

	_ = exec.Command("docker", "rm", "-f", container).Run()
	_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()

	if repoCache != "" && srcVol != "" {
		go d.autoSaveRepoBaseline(repoCache, srcVol)
	}

	writeFilesJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("stopping %s", container)})
}

// autoSaveRepoBaseline snapshots srcVol into the repo baseline cache in the
// background (a full volume copy — minutes for a multi-GB data root). Guarded by
// a re-check of Exists so two concurrent stops can't both create it. Logged to
// app_logs; failures are non-fatal (the project already stopped fine).
func (d *dashboardServer) autoSaveRepoBaseline(repoCache, srcVol string) {
	if dindcache.Exists(repoCache) {
		return // another stop beat us to it
	}
	d.applog().Log(applog.Entry{
		Category: applog.CatSystem, Event: "dind.baseline.autosave.start",
		Message: applog.Fmt("Auto-saving repo DinD baseline %q from %s", repoCache, srcVol),
		Status:  applog.StatusOK,
		Meta:    map[string]any{"cache": repoCache, "src": srcVol},
	})
	if _, err := dindcache.CreateFromVolume(repoCache, srcVol); err != nil {
		d.applog().Log(applog.Entry{
			Category: applog.CatSystem, Event: "dind.baseline.autosave.error",
			Message: applog.Fmt("Auto-save of repo baseline %q failed: %v", repoCache, err),
			Status:  applog.StatusError,
			Meta:    map[string]any{"cache": repoCache, "error": err.Error()},
		})
		return
	}
	d.applog().Log(applog.Entry{
		Category: applog.CatSystem, Event: "dind.baseline.autosave.done",
		Message: applog.Fmt("Saved repo DinD baseline %q — new projects from this repo will reuse it", repoCache),
		Status:  applog.StatusOK,
		Meta:    map[string]any{"cache": repoCache},
	})
}

// claudeReady reports whether a captured tmux pane shows Claude's input prompt
// ready to accept text. Claude's TUI draws a `❯` prompt line and a "bypass
// permissions" footer once it's up; either is a reliable "ready" signal and both
// only appear after boot completes (so we won't type into the boot log).
func claudeReady(pane string) bool {
	return strings.Contains(pane, "❯") ||
		strings.Contains(pane, "bypass permissions") ||
		strings.Contains(pane, "shift+tab to cycle")
}

// claimPendingPrompt atomically reads-and-clears a project's PendingPrompt,
// returning what it claimed (or "" if nothing was pending / read failed). The
// lock makes first-turn delivery fire EXACTLY once across the browser's
// concurrent /start + /populate-prompt calls (see promptClaimMu).
func (d *dashboardServer) claimPendingPrompt(workspace string) string {
	d.promptClaimMu.Lock()
	defer d.promptClaimMu.Unlock()
	projectDir := config.ProjectDirFor(workspace)
	cfg, err := config.ReadConfig(projectDir)
	if err != nil || strings.TrimSpace(cfg.PendingPrompt) == "" {
		return ""
	}
	prompt := cfg.PendingPrompt
	cfg.PendingPrompt = ""
	_ = config.WriteConfig(projectDir, cfg)
	return prompt
}

// handlePopulatePrompt types a prompt INTO the project's Claude input once the
// container's dev session is up. By default it does NOT submit (send-keys, no
// Enter) so the user reviews the pre-typed prompt and presses Enter themselves;
// with {submit:true} it also sends Enter to auto-start Claude on the prompt.
//
//	POST /p/<id>/populate-prompt   { "prompt": "...", "submit": false }
//
// The session may not exist yet (the container is still booting), so we poll in
// the background and return immediately; the prompt lands whenever Claude's
// session appears, or gives up after a bounded wait.
func (d *dashboardServer) handlePopulatePrompt(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Submit bool   `json:"submit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// The browser is explicitly driving delivery — CLAIM the pending prompt so the
	// auto-deliver-on-start path (handleStartProject) won't ALSO type it (the
	// browser calls both /start and /populate-prompt; without this the task lands
	// twice). We ignore the claimed value here and use the request's body.Prompt
	// (identical for the create flow, but the browser may override it, and honors
	// submit:false which auto-delivery can't).
	d.claimPendingPrompt(workspace)

	go deliverPromptToClaude(workspace, body.Prompt, body.Submit)

	writeFilesJSON(w, map[string]any{"ok": true, "message": "prompt will be typed into Claude once its input is ready"})
}

// deliverPromptToClaude types a prompt into the project's tmux-hosted Claude TUI
// once its input is ready, optionally submitting it. It BLOCKS (poll loop up to
// ~5 min) — callers run it in a goroutine.
//
// This is the one delivery path shared by the browser (POST /populate-prompt)
// and the backend auto-delivery on start (handleStartProject), so a project
// created HEADLESSLY — e.g. by the conductor via the API, with no browser to
// drive populate-prompt — still receives its task. It REQUIRES the container to
// run with tmux (LAUNCH_TMUX=1): the pane is what we capture/send-keys against.
func deliverPromptToClaude(workspace, prompt string, submit bool) {
	if strings.TrimSpace(prompt) == "" {
		return
	}
	sess := session.TmuxSessionNameForWorkspace(workspace)
	// The session existing is NOT enough: it's created by `docker run` in tmux,
	// but the container then boots (proxy, dockerd, launcher) for a while before
	// Claude's TUI actually appears and can accept input. Typing during that
	// window is lost. So poll the PANE CONTENT until Claude's input prompt is
	// drawn (its `❯` prompt / "bypass permissions" footer), then type.
	//
	// Up to ~5 min (cold boot + image build can be slow). Once ready, a short
	// settle, then send-keys.
	for i := 0; i < 300; i++ {
		time.Sleep(1 * time.Second)
		if !session.TmuxSessionExists(sess) {
			continue
		}
		out, _ := exec.Command("tmux", "capture-pane", "-t", sess, "-p").Output()
		if claudeReady(string(out)) {
			time.Sleep(1500 * time.Millisecond) // let the input line settle
			_ = exec.Command("tmux", "send-keys", "-t", sess, "--", prompt).Run()
			if submit {
				// Enter must be a SEPARATE send-keys after a short beat, or the
				// TUI can swallow it before the prompt text registers.
				time.Sleep(400 * time.Millisecond)
				_ = exec.Command("tmux", "send-keys", "-t", sess, "Enter").Run()
			}
			return
		}
	}
}
