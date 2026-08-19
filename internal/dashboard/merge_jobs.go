package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/scoutapp/corral/internal/config"
)

// The merge-job registry backs "Merge with host": a host `claude` that rebases a
// PR branch onto its base, resolves conflicts, waits for CI, and merges — run as
// a DETACHED background job, NOT tied to any WebSocket. This is what lets you
// navigate away from the merge and come back to it (the "Work" tab).
//
// Each job:
//   - runs the host claude under its OWN context (cancelled only by an explicit
//     delete, never by a socket closing),
//   - appends every streamed event to a transcript file (JSONL) so a re-opened
//     viewer replays history then live-tails, and the transcript survives a
//     dashboard restart,
//   - fans each event out to any attached WS viewers.
//
// An index file records job metadata so the Work tab can list jobs across a
// restart; a job whose process didn't survive the restart is marked interrupted.

// Merge-job status values.
const (
	mergeJobPreparing   = "preparing"   // cloning the host checkout
	mergeJobRunning     = "running"     // claude turn in progress
	mergeJobIdle        = "idle"        // between turns, awaiting steer / done streaming a turn
	mergeJobDone        = "done"        // finished (the last turn ended without error)
	mergeJobFailed      = "failed"      // a fatal error (checkout failed, etc.)
	mergeJobCanceled    = "canceled"    // explicitly deleted while running
	mergeJobInterrupted = "interrupted" // process didn't survive a dashboard restart
)

// Job kinds. The registry holds any detached host-Claude job; Kind distinguishes
// them so the Work tab and handlers can treat them appropriately.
const (
	jobKindMerge  = "merge"  // rebase-and-merge a PR (RepoName/PRNumber/Strategy set)
	jobKindWorker = "worker" // a conductor-spawned task worker (Title/Prompt set)
)

// mergeJob is one background host-Claude job. Despite the name it's the generic
// job type for the registry: a "merge" kind (rebase-and-merge a PR) or a
// "worker" kind (a conductor-spawned task Claude). Merge-specific fields
// (RepoName/PRNumber/Strategy/Branch/Checkout) are empty for workers; Title
// labels a worker (empty for merges, which label by repo #PR).
type mergeJob struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`  // merge | worker (empty legacy value = merge)
	Title     string `json:"title"` // worker label (workers only)
	PRID      int64  `json:"prId"`
	RepoID    string `json:"repoId"`
	OwnerName string `json:"ownerName"`
	PRNumber  int    `json:"prNumber"`
	RepoName  string `json:"repoName"`
	Strategy  string `json:"strategy"`
	Prompt    string `json:"prompt"`
	Checkout  string `json:"checkout"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"` // RFC3339

	Branch string `json:"branch"` // PR head branch (checked out)

	// lastEventUnix is the wall-clock (unix seconds) of the most recent streamed
	// event, updated in emit(). It drives the "working vs idle" activity signal:
	// output in the last few seconds → working, quiet → idle. Kept as an int64 so
	// it's cheap to read/write under the mutex; not persisted (activity is a
	// live-only notion).
	lastEventUnix int64

	// Runtime-only (not persisted directly; Status is what persists).
	mu          sync.Mutex
	cancel      context.CancelFunc
	sessionID   string
	subscribers map[chan mergeJobEvent]struct{}
	steer       chan string // follow-up prompts from a viewer
	closed      bool        // true once the job's run goroutine has fully exited
}

// mergeJobEvent is one streamed message plus a terminal marker. Type "" with
// Done=true signals end-of-stream to attached viewers.
type mergeJobEvent struct {
	Msg  chatServerMsg
	Done bool
}

// mergeJobRegistry owns the set of live jobs + their persistence.
type mergeJobRegistry struct {
	d    *dashboardServer
	mu   sync.Mutex
	jobs map[string]*mergeJob
}

func newMergeJobRegistry(d *dashboardServer) *mergeJobRegistry {
	return &mergeJobRegistry{d: d, jobs: map[string]*mergeJob{}}
}

// mergeJobsDir is where transcripts + the index live.
func mergeJobsDir() string { return filepath.Join(config.CorralHome(), "merge-jobs") }

// transcriptPath is a job's JSONL event log.
func transcriptPath(id string) string { return filepath.Join(mergeJobsDir(), id+".jsonl") }

// indexPath is the persisted metadata index for all jobs.
func mergeJobIndexPath() string { return filepath.Join(mergeJobsDir(), "index.json") }

// load reads the persisted index on dashboard start. Any job that was left
// "running"/"preparing"/"idle" clearly didn't survive the restart (its process
// is gone), so it's downgraded to interrupted. Best-effort: a missing/corrupt
// index just yields an empty registry.
func (r *mergeJobRegistry) load() {
	data, err := os.ReadFile(mergeJobIndexPath())
	if err != nil {
		return
	}
	var persisted []*mergeJob
	if json.Unmarshal(data, &persisted) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range persisted {
		if j.Kind == "" {
			j.Kind = jobKindMerge // legacy jobs predate the kind field
		}
		switch j.Status {
		case mergeJobRunning, mergeJobPreparing, mergeJobIdle:
			j.Status = mergeJobInterrupted
		}
		j.subscribers = map[chan mergeJobEvent]struct{}{}
		j.closed = true // no live process behind a reloaded job
		r.jobs[j.ID] = j
	}
}

// persist writes the metadata index (called after any status change). The
// transcript files are written incrementally as events stream.
func (r *mergeJobRegistry) persist() {
	r.mu.Lock()
	list := make([]*mergeJob, 0, len(r.jobs))
	for _, j := range r.jobs {
		list = append(list, j)
	}
	r.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })

	if err := os.MkdirAll(mergeJobsDir(), 0700); err != nil {
		return
	}
	// Marshal each job's persistable fields (the runtime mutex/chan fields are
	// unexported, so json ignores them).
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(mergeJobIndexPath(), data, 0600)
}

// list returns a snapshot of jobs, newest first, for the Work tab.
func (r *mergeJobRegistry) list() []*mergeJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*mergeJob, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (r *mergeJobRegistry) get(id string) *mergeJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[id]
}

// setStatus updates a job's status and re-persists the index.
func (r *mergeJobRegistry) setStatus(j *mergeJob, status string) {
	j.mu.Lock()
	j.Status = status
	j.mu.Unlock()
	r.persist()
}

// add registers a new job and persists.
func (r *mergeJobRegistry) add(j *mergeJob) {
	r.mu.Lock()
	r.jobs[j.ID] = j
	r.mu.Unlock()
	r.persist()
}

// remove deletes a job: cancels its process if running, drops it from the
// registry, and removes its transcript + checkout. Returns false if unknown.
func (r *mergeJobRegistry) remove(id string) bool {
	j := r.get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	checkout := j.Checkout
	j.mu.Unlock()

	r.mu.Lock()
	delete(r.jobs, id)
	r.mu.Unlock()

	if checkout != "" {
		_ = os.RemoveAll(checkout)
	}
	_ = os.Remove(transcriptPath(id))
	r.persist()
	return true
}

// subscribe attaches a viewer: it returns a channel of events and a function
// that replays the transcript-so-far into `send` before live events flow. The
// caller must call unsubscribe when done.
func (j *mergeJob) subscribe() chan mergeJobEvent {
	ch := make(chan mergeJobEvent, 256)
	j.mu.Lock()
	if j.subscribers == nil {
		j.subscribers = map[chan mergeJobEvent]struct{}{}
	}
	// A job that has already finished gets an immediate Done so the viewer knows
	// not to wait for live output after replay.
	finished := j.closed
	j.subscribers[ch] = struct{}{}
	j.mu.Unlock()
	if finished {
		ch <- mergeJobEvent{Done: true}
	}
	return ch
}

func (j *mergeJob) unsubscribe(ch chan mergeJobEvent) {
	j.mu.Lock()
	delete(j.subscribers, ch)
	j.mu.Unlock()
}

// emit appends an event to the transcript and fans it out to subscribers. This
// is the `send` used by the job's claude turn — signature matches runChatTurn's
// send (returns error; always nil, the job never fails a write mid-turn).
func (j *mergeJob) emit(m chatServerMsg) error {
	// Append to the transcript (JSONL). Best-effort — a transcript write failure
	// shouldn't stop the live stream.
	if line, err := json.Marshal(m); err == nil {
		if f, ferr := os.OpenFile(transcriptPath(j.ID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); ferr == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	j.mu.Lock()
	j.lastEventUnix = time.Now().Unix()
	if m.Type == "session" {
		j.sessionID = m.SessionID
	}
	j.mu.Unlock()
	j.fanout(mergeJobEvent{Msg: m})
	return nil
}

// activityIdleAfter is how long a job can go without a streamed event before it
// reads as "idle" rather than "working". A var so tests can shorten it.
var activityIdleAfter = 6 * time.Second

// activity reports "working" | "idle" for a still-live job (running/idle status),
// based on how recently it last emitted. For terminal statuses it returns "" —
// the caller shows the status itself. This is the server-side signal the Work-tab
// dot uses (no client-side DOM watching needed).
func (j *mergeJob) activity() string {
	j.mu.Lock()
	status := j.Status
	last := j.lastEventUnix
	j.mu.Unlock()
	if status != mergeJobRunning && status != mergeJobIdle && status != mergeJobPreparing {
		return ""
	}
	if last == 0 {
		return "working" // just started, nothing emitted yet — assume working
	}
	if time.Now().Unix()-last <= int64(activityIdleAfter.Seconds()) {
		return "working"
	}
	return "idle"
}

// fanout delivers an event to every subscriber, dropping to a job-closed marker
// when Done. Non-blocking per subscriber (buffered channel); a slow viewer that
// fills its buffer simply misses live events (it can re-attach to replay).
func (j *mergeJob) fanout(ev mergeJobEvent) {
	j.mu.Lock()
	subs := make([]chan mergeJobEvent, 0, len(j.subscribers))
	for ch := range j.subscribers {
		subs = append(subs, ch)
	}
	j.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// replayTranscript reads the on-disk transcript and calls send for each event.
// Used when a viewer attaches so it sees history before live output.
func replayTranscript(id string, send func(chatServerMsg) error) {
	f, err := os.Open(transcriptPath(id))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var m chatServerMsg
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			_ = send(m)
		}
	}
}

// finish marks the run goroutine as exited and notifies subscribers with a Done.
func (j *mergeJob) finish() {
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
	j.fanout(mergeJobEvent{Done: true})
}

// newMergeJobID makes a short, sortable-enough id. Uses the shared token helper;
// falls back to a PR-scoped stamp if that fails (Date is unavailable in some
// contexts, so we key on PR + a random token instead of a timestamp).
func newMergeJobID(prNumber int) string {
	tok, err := randomToken()
	if err != nil || tok == "" {
		return fmt.Sprintf("pr%d-job", prNumber)
	}
	if len(tok) > 12 {
		tok = tok[:12]
	}
	return fmt.Sprintf("pr%d-%s", prNumber, tok)
}

// newWorkerJobID makes a short id for a conductor worker job.
func newWorkerJobID() string {
	tok, err := randomToken()
	if err != nil || tok == "" {
		return "worker-job"
	}
	if len(tok) > 12 {
		tok = tok[:12]
	}
	return "worker-" + tok
}

// nowRFC3339 returns the current time formatted; isolated so tests can see it's
// the only time source here (Date.now-equivalent). Real time is fine on the host
// dashboard (unlike workflow scripts).
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
