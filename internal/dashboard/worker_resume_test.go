package dashboard

import (
	"strings"
	"testing"
)

// TestHostClaudeStaysGated is a guardrail test: host claude turns must ALWAYS run
// with --permission-mode default. Host claude (worker/merge jobs included) runs
// with the operator's privileges and is NOT sandboxed, so the permission prompt
// is a real safety boundary — it must never be bypassed. Fails loudly if anyone
// reintroduces a bypass into the argv.
func TestHostClaudeStaysGated(t *testing.T) {
	args := strings.Join(buildClaudeArgs("hi", []string{"Bash"}, ""), " ")
	if !strings.Contains(args, "--permission-mode default") {
		t.Fatalf("host claude must use --permission-mode default, got: %s", args)
	}
	if strings.Contains(args, "bypassPermissions") || strings.Contains(args, "dangerously-skip-permissions") {
		t.Fatalf("host claude must NEVER bypass permissions, got: %s", args)
	}
}

// TestWorkerContractPreamble locks in the resumable-fork contract: it tells the
// worker it's detached + NOT sandboxed (permissions still apply), to use only its
// granted tools (Bash/Monitor for waits), and how to self-wake via corral with
// its own id.
func TestWorkerContractPreamble(t *testing.T) {
	p := workerContractPreamble("worker-abc123")
	for _, want := range []string{
		"DETACHED", "process ENDS", "NOT sandboxed", "granted tools",
		"corral worker wake worker-abc123", "worker-abc123",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("preamble missing %q", want)
		}
	}
	// It must NOT claim permissions are bypassed (they aren't, on the host).
	if strings.Contains(p, "bypass") {
		t.Errorf("preamble must not tell workers permissions are bypassed")
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
