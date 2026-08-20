package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/applog"
)

// wakeDefaultPrompt is the continuation prompt used when a wake carries none.
const wakeDefaultPrompt = "Resume where you left off: check whether the background work you were waiting on has finished, then continue the task to completion."

// maxWakeDelay caps a scheduled self-wake so a runaway delay can't pin a job
// "working" forever. A worker waiting on something slower than this should wake
// periodically and re-check rather than schedule one enormous delay.
const maxWakeDelay = 30 * time.Minute

// handleMergeJobWake lets a DETACHED job (worker or host-merge) resume itself by
// enqueuing a continuation turn — the wire that makes a worker a true resumable
// fork. A worker that kicked off slow background work ends its turn with a wake
// request ("wake me in 30s") instead of stranding itself; when the timer fires
// (or immediately), the continuation is delivered as a steer and the job's run
// loop starts the next --resume turn with full prior context.
//
//	POST /merge-jobs/<id>/wake  { "prompt"?: "...", "inSeconds"?: 30 }
//	-> { ok, jobId, wakeInSeconds }
//
// inSeconds omitted/0 = wake now. Capped at maxWakeDelay. The wake is bound to
// the job's lifetime: if the job is removed before it fires, it's dropped.
func (d *dashboardServer) handleMergeJobWake(w http.ResponseWriter, r *http.Request, id string) {
	job := d.mergeJobs.get(id)
	if job == nil {
		http.Error(w, "unknown job", http.StatusNotFound)
		return
	}
	var body struct {
		Prompt    string `json:"prompt"`
		InSeconds int    `json:"inSeconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional

	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = wakeDefaultPrompt
	}
	delay := time.Duration(body.InSeconds) * time.Second
	if delay < 0 {
		delay = 0
	}
	if delay > maxWakeDelay {
		delay = maxWakeDelay
	}

	d.applog().Log(applog.Entry{
		Category: applog.CatAI, Event: "worker.wake.schedule",
		Message: applog.Fmt("Worker %s self-wake in %s", id, delay),
		Status:  applog.StatusOK,
		Meta:    map[string]any{"job": id, "inSeconds": int(delay.Seconds())},
	})

	if delay == 0 {
		job.queueSteer(prompt)
	} else {
		go d.deliverWakeAfter(job, prompt, delay)
	}
	writeFilesJSON(w, map[string]any{"ok": true, "jobId": id, "wakeInSeconds": int(delay.Seconds())})
}

// deliverWakeAfter enqueues the continuation steer after delay, unless the job is
// cancelled first (removed). Bound to the job's own context so a removed job's
// pending wake is dropped rather than resurrecting it.
func (d *dashboardServer) deliverWakeAfter(job *mergeJob, prompt string, delay time.Duration) {
	job.mu.Lock()
	ctx := job.ctxOrNil()
	job.mu.Unlock()
	t := time.NewTimer(delay)
	defer t.Stop()
	if ctx == nil {
		<-t.C
		job.queueSteer(prompt)
		return
	}
	select {
	case <-ctx.Done():
		return // job removed before the wake fired
	case <-t.C:
		job.queueSteer(prompt)
	}
}
