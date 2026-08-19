package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/prreview"
	"github.com/scoutapp/corral/internal/repos"
)

// handlePRMergeHostStart creates and launches a "Merge with host" background
// job: it prepares a throwaway host checkout of the PR branch and runs a host
// `claude` (act-capable — Bash/Edit, so it can rebase, resolve conflicts, wait
// for CI, and `gh pr merge`) with the editable pr.merge prompt. The job runs
// DETACHED — under its own context, not the request's — so it keeps going after
// this handler returns and after the browser navigates away. Returns the job id;
// the client attaches to /merge-jobs/<id>/ws to watch it.
//
//	POST /prs/<prId>/merge-host/start  ->  { jobId }
//
// NOT SANDBOXED: runs the operator's own host `claude` with Bash against a real
// git checkout using host git/gh credentials. It's the fast path (no container);
// the UI warns that it isn't sandboxed.
func (d *dashboardServer) handlePRMergeHostStart(w http.ResponseWriter, r *http.Request, prID int64) {
	job, err := d.startMergeJob(prID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeFilesJSON(w, map[string]any{"jobId": job.ID})
}

// startMergeJob builds, registers, and launches a detached host-merge job for a
// PR, returning it. Shared by the HTTP start endpoint and the WS alias. Errors
// are plain (the callers map them to HTTP). It does NOT block on the job — the
// run goroutine streams asynchronously.
func (d *dashboardServer) startMergeJob(prID int64) (*mergeJob, error) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		return nil, fmt.Errorf("the `claude` CLI could not be located — install Claude Code and restart the dashboard")
	}
	s, err := d.getStore()
	if err != nil {
		return nil, fmt.Errorf("database unavailable: %w", err)
	}
	svc := prreview.New(s)

	repoID, err := svc.RepoIDForPR(prID)
	if err != nil {
		return nil, fmt.Errorf("unknown PR")
	}
	ownerName := d.ownerNameForPR(svc, prID)
	if ownerName == "" {
		return nil, fmt.Errorf("repo is not a GitHub remote")
	}
	mi, err := svc.PRMergeInfo(prID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mi.HeadRef) == "" {
		return nil, fmt.Errorf("PR has no known branch — refresh it from GitHub first")
	}

	repoName, defaultBranch := ownerName, "main"
	if repo, gerr := repos.Get(repoID); gerr == nil {
		if repo.Name != "" {
			repoName = repo.Name
		}
		if repo.DefaultBranch != "" {
			defaultBranch = repo.DefaultBranch
		}
	}
	allowed, preferred := d.resolveRepoMergeMethods(repoID, ownerName)
	strategy := effectiveMergeStrategy(preferred, allowed)
	mergePrompt := d.renderMergePrompt(repoID, prPromptSlots(repoName, mi, strategy, defaultBranch))

	job := &mergeJob{
		ID:        newMergeJobID(mi.Number),
		Kind:      jobKindMerge,
		PRID:      prID,
		RepoID:    repoID,
		OwnerName: ownerName,
		PRNumber:  mi.Number,
		RepoName:  repoName,
		Strategy:  strategy,
		Prompt:    mergePrompt,
		Checkout:  hostMergeCheckoutPath(prID),
		Branch:    mi.HeadRef,
		Status:    mergeJobPreparing,
		CreatedAt: nowRFC3339(),

		subscribers: map[chan mergeJobEvent]struct{}{},
	}
	d.mergeJobs.add(job)

	d.applog().Log(applog.Entry{
		Category: applog.CatPRAction, Event: "pr.merge_host.start",
		Message: applog.Fmt("Merge-with-host started for %s#%d (%s)", ownerName, mi.Number, strategy),
		RepoID:  repoID, Status: applog.StatusOK,
		Meta: map[string]any{"owner": ownerName, "pr": mi.Number, "strategy": strategy, "job": job.ID},
	})

	go d.runMergeJob(claudeBin, job)
	return job, nil
}

// runMergeJob is the detached lifecycle of a host-merge job: prepare the
// checkout, run the merge prompt as the first claude turn, then remain "idle"
// (the process is gone between turns, but the job stays so a viewer can send a
// follow-up steer, which spawns another turn). The job's context is its own —
// cancelled only when the job is removed.
func (d *dashboardServer) runMergeJob(claudeBin string, job *mergeJob) {
	ctx, cancel := context.WithCancel(context.Background())
	job.mu.Lock()
	job.cancel = cancel
	job.mu.Unlock()
	defer cancel()
	defer job.finish()

	// Capture this merge's conversation into the conversations DB (best-effort;
	// never blocks job.emit / the Work-tab stream). Origin carries PR linkage.
	capt, send, finalize := d.captureSend(ctx, convOrigin{
		Kind: jobKindMerge, OriginID: job.ID,
		RepoID: job.RepoID, PRNumber: job.PRNumber,
	}, job.emit)
	defer finalize(mergeJobDone)

	// Prepare the throwaway host checkout on the PR branch (CloneLocal repoints
	// origin at the real remote, so push / gh merge use host git/gh auth).
	send(chatServerMsg{Type: "text", Text: fmt.Sprintf("Preparing a host checkout of %s on %s…\n", job.RepoName, prBranchLabel(job))})
	_ = os.RemoveAll(job.Checkout)
	if err := repos.CloneLocal(job.RepoID, job.Checkout, prMergeBranch(d, job)); err != nil {
		send(chatServerMsg{Type: "error", Text: "failed to prepare host checkout: " + err.Error()})
		d.mergeJobs.setStatus(job, mergeJobFailed)
		return
	}

	tools := []string{"Bash", "Read", "Edit", "Write", "Grep", "Glob"}

	// runTurn streams one claude turn's events into the job (transcript + fanout + capture).
	runTurn := func(prompt string) bool {
		d.mergeJobs.setStatus(job, mergeJobRunning)
		capt.recordPrompt(prompt)
		job.mu.Lock()
		if job.ConvID == 0 {
			job.ConvID = capt.ConvID() // DB is the replay source of truth
		}
		sid := job.sessionID
		job.mu.Unlock()
		newSession, canceled := d.runChatTurn(ctx, claudeBin, job.Checkout, tools, prompt, sid, send)
		job.mu.Lock()
		job.sessionID = newSession
		job.mu.Unlock()
		if canceled {
			send(chatServerMsg{Type: "canceled"})
		}
		send(chatServerMsg{Type: "turn_end"})
		return canceled
	}

	canceled := runTurn(job.Prompt)
	if canceled || ctx.Err() != nil {
		d.mergeJobs.setStatus(job, mergeJobCanceled)
		return
	}
	// First turn done. The job now rests in "idle" — its work is finished, but it
	// stays available so a viewer can send a follow-up steer (e.g. "resolve that
	// conflict the other way"), which spawns another turn. The run goroutine lives
	// until the job is explicitly removed (ctx cancelled by DELETE). Idle is the
	// resting success state; "done" is reserved for when the user closes the job.
	d.mergeJobs.setStatus(job, mergeJobIdle)

	for {
		select {
		case <-ctx.Done():
			// Removed while idle — the DELETE path already cleaned up; nothing to do.
			return
		case p := <-job.steerCh():
			if strings.TrimSpace(p) == "" {
				continue
			}
			if runTurn(p) || ctx.Err() != nil {
				return
			}
			d.mergeJobs.setStatus(job, mergeJobIdle)
		}
	}
}

// steerCh lazily creates the job's steer channel (follow-up prompts from a
// viewer). Guarded so concurrent attaches share one channel.
func (j *mergeJob) steerCh() chan string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.steer == nil {
		j.steer = make(chan string, 8)
	}
	return j.steer
}

// queueSteer accepts a follow-up prompt from a viewer and, crucially, gives
// visible feedback. The run loop only *reads* steerCh() while parked idle
// between turns, so a steer sent to a busy worker (mid-turn) sits buffered and
// silent — which reads to the user as "my message didn't send". We instead emit
// a transcript line so every viewer sees the message was received, whether it
// runs now (worker idle) or is queued until the current turn finishes. The steer
// buffer is large; if it ever fills, say so rather than dropping silently.
func (j *mergeJob) queueSteer(prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	j.mu.Lock()
	busy := j.Status == mergeJobRunning || j.Status == mergeJobPreparing
	j.mu.Unlock()

	select {
	case j.steerCh() <- prompt:
		if busy {
			// The run loop is mid-turn and won't read this until the turn ends.
			j.emit(chatServerMsg{Type: "text",
				Text: "\n💬 Message received — the worker is mid-step; it'll pick this up when the current step finishes.\n"})
		}
		// When idle, the loop consumes it immediately and the resulting turn is
		// its own visible acknowledgment, so no extra line is needed.
	default:
		// Buffer full (many steers stacked up on a stuck turn). Never drop silently.
		j.emit(chatServerMsg{Type: "error",
			Text: "\n⚠️ Couldn't queue your message — several are already waiting for the worker to finish its current step. Try again once it catches up.\n"})
	}
}

// handlePRMergeHostWS (kept for the existing PR-page drawer) attaches to this
// PR's live host-merge job — starting one on the fly if none is running yet, so
// a client that just opens this WS (the pre-Work-tab drawer) still kicks off and
// streams the merge. The primary flow is start + /merge-jobs/<id>/ws.
func (d *dashboardServer) handlePRMergeHostWS(w http.ResponseWriter, r *http.Request, prID int64) {
	var job *mergeJob
	for _, j := range d.mergeJobs.list() {
		if j.PRID == prID && (j.Status == mergeJobRunning || j.Status == mergeJobIdle || j.Status == mergeJobPreparing) {
			job = j
			break
		}
	}
	if job == nil {
		// No live job → start one, then attach. Reuse the start path's setup by
		// building the job here; on any prep error, report it over HTTP (pre-upgrade).
		newJob, err := d.startMergeJob(prID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job = newJob
	}
	d.attachMergeJobWS(w, r, job)
}

// handleMergeJobWS attaches a viewer to a job by id: replays the transcript, then
// live-tails events, and accepts follow-up steer prompts. Detaching (socket
// close) leaves the job running.
//
//	GET /merge-jobs/<id>/ws
func (d *dashboardServer) handleMergeJobWS(w http.ResponseWriter, r *http.Request, id string) {
	job := d.mergeJobs.get(id)
	if job == nil {
		http.Error(w, "unknown merge job", http.StatusNotFound)
		return
	}
	d.attachMergeJobWS(w, r, job)
}

// attachMergeJobWS is the shared viewer transport: upgrade → replay transcript →
// stream live events + forward steer prompts to the job.
func (d *dashboardServer) attachMergeJobWS(w http.ResponseWriter, r *http.Request, job *mergeJob) {
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(m chatServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(m)
	}

	// Replay history so a freshly-opened viewer sees what happened, then subscribe
	// to live events. subscribe() may immediately deliver a Done for a finished
	// job (after replay the viewer then knows the stream is complete).
	d.replayJob(job, send)
	ch := job.subscribe()
	defer job.unsubscribe(ch)

	// Reader goroutine: forward steer prompts / cancel from the browser.
	go func() {
		for {
			_, data, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			var msg chatClientMsg
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if msg.Action == "cancel" {
				// A viewer "cancel" only detaches this socket; the job keeps
				// running (that's the whole point). Full teardown is an explicit
				// DELETE /merge-jobs/<id>. So ignore it here.
				continue
			}
			if strings.TrimSpace(msg.Prompt) != "" {
				job.queueSteer(msg.Prompt)
			}
		}
	}()

	for ev := range ch {
		if ev.Done {
			_ = send(chatServerMsg{Type: "turn_end"})
			return
		}
		if send(ev.Msg) != nil {
			return
		}
	}
}

// handleMergeJobsList returns the job list for the Work tab.
//
//	GET /merge-jobs
func (d *dashboardServer) handleMergeJobsList(w http.ResponseWriter, r *http.Request) {
	list := d.mergeJobs.list()
	out := make([]map[string]any, 0, len(list))
	for _, j := range list {
		kind := j.Kind
		if kind == "" {
			kind = jobKindMerge
		}
		out = append(out, map[string]any{
			"id":        j.ID,
			"kind":      kind,
			"title":     j.Title,
			"prId":      j.PRID,
			"repoId":    j.RepoID,
			"prNumber":  j.PRNumber,
			"repoName":  j.RepoName,
			"strategy":  j.Strategy,
			"status":    j.Status,
			"activity":  j.activity(), // "working" | "idle" | "" (terminal)
			"createdAt": j.CreatedAt,
		})
	}
	writeFilesJSON(w, map[string]any{"jobs": out})
}

// handleMergeJobDelete removes a job: cancels it if running and cleans up.
//
//	DELETE /merge-jobs/<id>
func (d *dashboardServer) handleMergeJobDelete(w http.ResponseWriter, r *http.Request, id string) {
	if !d.mergeJobs.remove(id) {
		http.Error(w, "unknown merge job", http.StatusNotFound)
		return
	}
	writeFilesJSON(w, map[string]any{"ok": true})
}

// prBranchLabel / prMergeBranch resolve the PR branch for messages / checkout.
// The branch is stored on the PR row; re-read defensively.
func prBranchLabel(job *mergeJob) string { return job.Branch }
func prMergeBranch(d *dashboardServer, job *mergeJob) string {
	if job.Branch != "" {
		return job.Branch
	}
	if s, err := d.getStore(); err == nil {
		if mi, err := prreview.New(s).PRMergeInfo(job.PRID); err == nil {
			job.Branch = mi.HeadRef
		}
	}
	return job.Branch
}

// hostMergeCheckoutPath is the throwaway host checkout dir for a merge-with-host
// job, under CorralHome keyed by PR id.
func hostMergeCheckoutPath(prID int64) string {
	return filepath.Join(config.CorralHome(), "merge-host", fmt.Sprintf("pr-%d", prID))
}
