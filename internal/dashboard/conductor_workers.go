package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/scoutapp/corral/internal/applog"
)

// Conductor workers turn the global Claude into a conductor: it delegates tasks
// by spawning fresh, independent worker Claudes (NOT subagents), each a detached
// headless `claude -p` job in the shared registry. Each worker gets its own tab
// in the Work panel, streams into the same transcript/attach machinery as merge
// jobs, and reports the same working/idle activity. Workers run in a neutral
// dir (~/.corral) with the global chat's tool capability; the conductor passes
// all task context in the prompt.

// workerContractPreamble is prepended to every worker's FIRST-turn prompt (not to
// later human steers). A worker runs as a headless `claude -p` turn: when the
// turn's output ends, the process EXITS — the job then sits idle until a human
// sends a steer in the Work tab. Nothing auto-resumes it. So a worker that
// backgrounds long work and ends its message "waiting to be resumed" strands
// itself (the exact failure that left an app un-booted: the worker parked on a
// 4GB image transfer, ended its turn, and no notification could restart it).
// This contract tells the worker to finish the job WITHIN the turn instead.
const workerContractPreamble = "IMPORTANT — how you run: you are a single headless Claude turn (`claude -p`), " +
	"not an interactive session. When your reply ends, your process ENDS; nothing will " +
	"automatically resume you, and background jobs / scheduled wake-ups you start will NOT " +
	"call you back. Therefore: do the whole task to completion WITHIN this turn. Do NOT " +
	"background a long step (image pull/transfer, build, install) and end your message " +
	"expecting to continue later — instead BLOCK in-turn until it finishes (wait on it " +
	"synchronously, e.g. `wait`/a foreground command), then proceed to the next step. Only " +
	"end your turn once the task is actually done (e.g. the app is up and serving) or you are " +
	"truly blocked and need the human. Prefer a plain foreground/blocking wait over tools that " +
	"may require interactive approval. If a step is genuinely too long to finish now, say so " +
	"explicitly and state what remains, rather than silently parking.\n\n---\n\nTASK:\n\n"

// handleConductorWorkerCreate: POST /api/conductor/workers { prompt, title? }
// spawns a detached worker Claude and returns its job id. The worker starts
// immediately on the given prompt; watch/steer it in the Work tab.
func (d *dashboardServer) handleConductorWorkerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	// Cross-origin linkage: if a captured Claude drove this request (via corral
	// api), the parent conversation id rides in on this header — thread it so the
	// worker's own conversation chains back to it.
	job, err := d.startWorkerJob(body.Prompt, body.Title, parentConvFromRequest(r), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeFilesJSON(w, map[string]any{"jobId": job.ID, "title": job.Title})
}

// startWorkerJob builds, registers, and launches a detached worker Claude job,
// returning it. The worker runs headless (`claude -p`) in the neutral global-chat
// dir with the global chat capability's tools. Errors are plain (the caller maps
// to HTTP); the run itself streams asynchronously.
func (d *dashboardServer) startWorkerJob(prompt, title string, parentConvID int64, captureKind string) (*mergeJob, error) {
	claudeBin, err := resolveClaudeBin()
	if err != nil {
		return nil, fmt.Errorf("the `claude` CLI could not be located — install Claude Code and restart the dashboard")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "task"
	}

	job := &mergeJob{
		ID:                   newWorkerJobID(),
		Kind:                 jobKindWorker,
		Title:                title,
		Prompt:               workerContractPreamble + prompt,
		Status:               mergeJobRunning,
		CreatedAt:            nowRFC3339(),
		ParentConversationID: parentConvID,
		CaptureKind:          captureKind,

		subscribers: map[chan mergeJobEvent]struct{}{},
	}
	d.mergeJobs.add(job)

	d.applog().Log(applog.Entry{
		Category: applog.CatAI, Event: "conductor.worker.start",
		Message: applog.Fmt("Conductor worker started: %s", title),
		Status:  applog.StatusOK,
		Meta:    map[string]any{"job": job.ID, "title": title},
	})

	go d.runWorkerJob(claudeBin, job)
	return job, nil
}

// runWorkerJob is the detached lifecycle of a worker: run the task prompt as the
// first claude turn in the neutral dir, then rest idle awaiting optional steer
// turns (like a merge job). Cancelled only when the job is removed.
func (d *dashboardServer) runWorkerJob(claudeBin string, job *mergeJob) {
	ctx, cancel := context.WithCancel(context.Background())
	job.mu.Lock()
	job.cancel = cancel
	job.mu.Unlock()
	defer cancel()
	defer job.finish()

	// Tools follow the global chat capability (readonly vs act), gated the same way
	// the global assistant is. Workers run in the neutral global-chat dir.
	cap, ok := d.ChatCapability()
	tools := globalChatTools(cap, ok)
	workdir := globalChatDir()

	// Capture this worker's conversation into the conversations DB (best-effort;
	// never blocks job.emit / the Work-tab stream). One conversation per job.
	// CaptureKind lets a specialized worker (e.g. log-analysis) tag its
	// conversation with a distinct origin; defaults to "worker".
	originKind := job.CaptureKind
	if originKind == "" {
		originKind = jobKindWorker
	}
	capt, send, finalize := d.captureSend(ctx, convOrigin{
		Kind: originKind, OriginID: job.ID,
		ParentConversationID: job.ParentConversationID,
	}, job.emit)
	defer finalize(mergeJobDone)

	runTurn := func(prompt string) bool {
		d.mergeJobs.setStatus(job, mergeJobRunning)
		capt.recordPrompt(prompt)
		job.mu.Lock()
		if job.ConvID == 0 {
			job.ConvID = capt.ConvID() // DB is the replay source of truth
		}
		sid := job.sessionID
		job.mu.Unlock()
		newSession, canceled := d.runChatTurn(ctx, claudeBin, workdir, tools, prompt, sid, send)
		job.mu.Lock()
		job.sessionID = newSession
		job.mu.Unlock()
		if canceled {
			send(chatServerMsg{Type: "canceled"})
		}
		send(chatServerMsg{Type: "turn_end"})
		return canceled
	}

	if runTurn(job.Prompt) || ctx.Err() != nil {
		d.mergeJobs.setStatus(job, mergeJobCanceled)
		return
	}
	d.mergeJobs.setStatus(job, mergeJobIdle)

	for {
		select {
		case <-ctx.Done():
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
