package dashboard

import (
	"context"
	"strings"
	"testing"
)

// TestBypassPermissionsArgs verifies a detached turn's ctx flips the claude
// permission mode to bypassPermissions (so a worker's tools don't hit an approval
// prompt no human can answer), while a plain ctx stays on default.
func TestBypassPermissionsArgs(t *testing.T) {
	if bypassPermissionsFrom(context.Background()) {
		t.Fatal("plain ctx should NOT bypass permissions")
	}
	if !bypassPermissionsFrom(withBypassPermissions(context.Background())) {
		t.Fatal("withBypassPermissions ctx should bypass")
	}

	def := strings.Join(buildClaudeArgs("hi", nil, "", "default"), " ")
	if !strings.Contains(def, "--permission-mode default") {
		t.Fatalf("default mode missing: %s", def)
	}
	byp := strings.Join(buildClaudeArgs("hi", nil, "", "bypassPermissions"), " ")
	if !strings.Contains(byp, "--permission-mode bypassPermissions") {
		t.Fatalf("bypass mode missing: %s", byp)
	}
	// Empty mode falls back to default (so existing callers are unaffected).
	empty := strings.Join(buildClaudeArgs("hi", nil, "", ""), " ")
	if !strings.Contains(empty, "--permission-mode default") {
		t.Fatalf("empty mode should default: %s", empty)
	}
}

// TestWorkerContractPreamble locks in the resumable-fork contract: it tells the
// worker it's detached, that self-wake is via corral (not its own harness), and
// embeds THIS worker's job id + the exact wake command.
func TestWorkerContractPreamble(t *testing.T) {
	p := workerContractPreamble("worker-abc123")
	for _, want := range []string{
		"DETACHED", "process ENDS", "corral worker wake worker-abc123", "worker-abc123",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("preamble missing %q", want)
		}
	}
	// The caller's task must survive after the contract.
	composed := p + "Boot the app."
	if !strings.HasPrefix(composed, "IMPORTANT") || !strings.HasSuffix(composed, "Boot the app.") {
		t.Fatal("composition must be contract-then-task")
	}
}

// TestWorkerWakeEnqueuesSteer verifies an immediate wake enqueues a continuation
// steer onto the job (the wire that resumes a detached worker).
func TestWorkerWakeEnqueuesSteer(t *testing.T) {
	d := newDashboardServer("sess")
	job := &mergeJob{ID: "worker-wk", Kind: jobKindWorker, Status: mergeJobIdle, subscribers: map[chan mergeJobEvent]struct{}{}}
	d.mergeJobs.add(job)

	// deliver an immediate wake (delay 0) with a custom prompt.
	job.queueSteer("") // ensure channel exists / no-op
	d.deliverWakeAfter(job, "continue now", 0)

	select {
	case got := <-job.steerCh():
		if got != "continue now" {
			t.Fatalf("wake steer = %q", got)
		}
	default:
		t.Fatal("wake did not enqueue a steer")
	}
}
