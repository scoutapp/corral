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
// later steers). A worker runs as a detached headless `claude -p` turn: when the
// turn ends the process EXITS, and nothing in the model's own harness
// (ScheduleWakeup, background-task notifications) can re-invoke a detached
// process. What CAN resume it is corral: `corral worker wake`. So a worker must
// either finish in-turn or explicitly schedule its own wake before ending —
// never end with work "in flight, to be continued" and no wake, which strands it
// (the bug that left an app un-booted).
//
// NOTE on permissions: a worker runs on the HOST with the operator's privileges
// and is NOT sandboxed, so permission prompts still apply (they are a real
// guardrail here — nothing is bypassed). With "act" capability a worker is
// granted Read/Grep/Glob + Bash + Monitor, so it CAN run a bounded wait/poll loop
// (Bash `until …; do sleep; done`, or Monitor) without an approval prompt. It
// must still only use those granted tools — reaching for anything ungranted would
// block on approval no one can answer.
func workerContractPreamble(jobID string) string {
	return "IMPORTANT — how you run: you are a DETACHED headless Claude turn (`claude -p`), " +
		"not interactive. When your reply ends, your process ENDS. Your own ScheduleWakeup / " +
		"background-task notifications do NOT re-invoke you (they need an interactive harness you " +
		"don't have). The ONLY thing that resumes you is corral. So NEVER end a turn with work " +
		"still in flight and no plan to continue.\n" +
		"You run on the HOST and are NOT sandboxed, so permission prompts still apply — and there's " +
		"no human at you to answer one. Use ONLY your granted tools: Read/Grep/Glob, plus Bash and " +
		"Monitor when you have act capability. Those cover waiting/polling; do not reach for an " +
		"ungranted tool, which would block on approval and strand you.\n" +
		"Two valid ways to handle a long step (image pull/transfer, build, install):\n" +
		"  (a) BLOCK on it in-turn — run it in the foreground, or poll with Bash " +
		"(`until …; do sleep N; done`) / Monitor, then proceed once it's done.\n" +
		"  (b) Start it in the BACKGROUND, then run `corral worker wake " + jobID + " --in <secs>` " +
		"(e.g. --in 30) via Bash and end the turn. Corral re-invokes you after the delay with full " +
		"context so you can check on it and continue; repeat the wake if it's still going.\n" +
		"Only end WITHOUT a wake when the task is actually done, or you're truly blocked and must " +
		"report to the human. This job's id is `" + jobID + "`.\n" +
		"MAKE EXPENSIVE WORK REUSABLE: Corral snapshots a project's inner-docker on clean stop into " +
		"a per-repo baseline that future projects reuse — but a snapshot captures IMAGES and NAMED " +
		"VOLUMES, not a running container's writable layer. So don't `bundle install`/`npm ci`/`pip " +
		"install` inside a bare base-image container and leave it there — it'll be lost. Install deps " +
		"into a NAMED VOLUME the app mounts (e.g. `docker run -v <app>-bundle:/usr/local/bundle …`) or " +
		"`docker commit` the prepared container to an image. That way the slow dep-install is captured " +
		"and the next project from this repo skips it.\n\n---\n\nTASK:\n\n"
}

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

	jobID := newWorkerJobID()
	job := &mergeJob{
		ID:                   jobID,
		Kind:                 jobKindWorker,
		Title:                title,
		Prompt:               workerContractPreamble(jobID) + prompt,
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
	job.ctx = ctx // stored so a self-wake can bind to the job's lifetime
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
