package automations

import (
	"context"
	"testing"
)

// newChainRunner wires a runner whose "echo" executor echoes a var or fails on
// {"fail":true} (reused from runner_test.go's echoExecutor).
func newChainRunner(t *testing.T) (*Runner, *Service) {
	t.Helper()
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	return NewRunner(svc, reg), svc
}

func TestFireEventPrimaryOnly(t *testing.T) {
	r, svc := newChainRunner(t)
	primary, _ := svc.CreateAction(Action{Name: "builtin", Kind: "echo", Spec: `{"var":"x"}`})

	res, err := r.FireEvent(context.Background(), EventPRApprove, primary.ID,
		RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	if res.Primary.Output != "done" {
		t.Errorf("primary output wrong: %q", res.Primary.Output)
	}
	if len(res.Hooks) != 0 {
		t.Errorf("expected no secondary hooks, got %d", len(res.Hooks))
	}
	// One combined run recorded.
	if runs, _ := svc.RecentRuns(5); len(runs) != 1 || runs[0].Status != StatusOK {
		t.Errorf("expected 1 ok run, got %+v", runs)
	}
}

func TestFireEventSecondarySuccess(t *testing.T) {
	r, svc := newChainRunner(t)
	primary, _ := svc.CreateAction(Action{Name: "builtin", Kind: "echo", Spec: `{"var":"x"}`})
	notify, _ := svc.CreateAction(Action{Name: "notify", Kind: "echo", Spec: `{"var":"x"}`})
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "action", TargetID: notify.ID, Enabled: true})

	res, _ := r.FireEvent(context.Background(), EventPRApprove, primary.ID,
		RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "ok"}})
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	if len(res.Hooks) != 1 || res.Hooks[0].Status != StatusOK {
		t.Errorf("secondary hook not run ok: %+v", res.Hooks)
	}
}

func TestFireEventSecondaryFailureIsPartial(t *testing.T) {
	r, svc := newChainRunner(t)
	primary, _ := svc.CreateAction(Action{Name: "builtin", Kind: "echo", Spec: `{"var":"x"}`})
	boom, _ := svc.CreateAction(Action{Name: "boom", Kind: "echo", Spec: `{"fail":true}`})
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "action", TargetID: boom.ID, Enabled: true})

	res, _ := r.FireEvent(context.Background(), EventPRApprove, primary.ID,
		RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "ok"}})

	// Primary succeeded, so the OPERATION did not fail…
	if res.PrimaryFailed() {
		t.Fatal("primary should not have failed")
	}
	// …but the chain is partial because a secondary failed.
	if res.Status != StatusPartial {
		t.Fatalf("expected partial, got %q", res.Status)
	}
	if runs, _ := svc.RecentRuns(1); runs[0].Status != StatusPartial {
		t.Errorf("run should record partial, got %q", runs[0].Status)
	}
}

func TestFireEventPrimaryFailureIsError(t *testing.T) {
	r, svc := newChainRunner(t)
	primary, _ := svc.CreateAction(Action{Name: "builtin", Kind: "echo", Spec: `{"fail":true}`})
	notify, _ := svc.CreateAction(Action{Name: "notify", Kind: "echo", Spec: `{"var":"x"}`})
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "action", TargetID: notify.ID, Enabled: true})

	res, _ := r.FireEvent(context.Background(), EventPRApprove, primary.ID, RunContext{RepoID: "repo-A"})
	if !res.PrimaryFailed() || res.Status != StatusError {
		t.Fatalf("expected error status + PrimaryFailed, got %q failed=%v", res.Status, res.PrimaryFailed())
	}
	// Secondary still ran (best-effort), and is recorded.
	if len(res.Hooks) != 1 {
		t.Errorf("secondary should still run even when primary failed: %+v", res.Hooks)
	}
}

func TestFireEventNoPrimary(t *testing.T) {
	r, svc := newChainRunner(t)
	notify, _ := svc.CreateAction(Action{Name: "notify", Kind: "echo", Spec: `{"var":"x"}`})
	mustHook(t, svc, Hook{Event: EventPREnter, TargetKind: "action", TargetID: notify.ID, Enabled: true})

	// A pure-notification event: no built-in primary (pass 0).
	res, _ := r.FireEvent(context.Background(), EventPREnter, 0,
		RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "hi"}})
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	if len(res.Hooks) != 1 || res.Hooks[0].Output != "hi" {
		t.Errorf("notification hook not run: %+v", res.Hooks)
	}
}

func TestFireSecondaryNoHooksNoRun(t *testing.T) {
	r, svc := newChainRunner(t)
	// No hooks bound → no run recorded, status ok.
	res, err := r.FireSecondary(context.Background(), EventPRApprove, RunContext{RepoID: "repo-A"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Errorf("expected ok, got %q", res.Status)
	}
	if runs, _ := svc.RecentRuns(5); len(runs) != 0 {
		t.Errorf("no hooks should record no run, got %d", len(runs))
	}
}

func TestFireSecondaryPartial(t *testing.T) {
	r, svc := newChainRunner(t)
	ok, _ := svc.CreateAction(Action{Name: "ok", Kind: "echo", Spec: `{"var":"x"}`})
	boom, _ := svc.CreateAction(Action{Name: "boom", Kind: "echo", Spec: `{"fail":true}`})
	mustHook(t, svc, Hook{Event: EventPRMerge, TargetKind: "action", TargetID: ok.ID, Position: 0, Enabled: true})
	mustHook(t, svc, Hook{Event: EventPRMerge, TargetKind: "action", TargetID: boom.ID, Position: 1, Enabled: true})

	res, _ := r.FireSecondary(context.Background(), EventPRMerge, RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "hi"}})
	if res.Status != StatusPartial {
		t.Fatalf("expected partial (one secondary failed), got %q", res.Status)
	}
	if len(res.Hooks) != 2 {
		t.Errorf("both secondaries should have run, got %d", len(res.Hooks))
	}
	if runs, _ := svc.RecentRuns(1); len(runs) != 1 || runs[0].Status != StatusPartial {
		t.Error("partial run should be recorded")
	}
}

func TestMarshalContextVars(t *testing.T) {
	got := MarshalContextVars(map[string]any{"n": 42, "s": "x", "b": true})
	if got["n"] != "42" || got["s"] != "x" || got["b"] != "true" {
		t.Errorf("flatten wrong: %+v", got)
	}
}
