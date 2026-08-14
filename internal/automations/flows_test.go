package automations

import (
	"context"
	"testing"
)

func TestCreateFlowWithSteps(t *testing.T) {
	svc := newService(t)
	a1, _ := svc.CreateAction(Action{Name: "one", Kind: "echo", Spec: `{"var":"x"}`})
	a2, _ := svc.CreateAction(Action{Name: "two", Kind: "echo", Spec: `{"var":"y"}`})

	f, err := svc.CreateFlow(Flow{Name: "pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: a1.ID, StepKey: "first"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: a2.ID, StepKey: "second", DependsOn: []string{"first"}})

	got, err := svc.Flow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(got.Steps))
	}
	if got.Steps[0].StepKey != "first" || got.Steps[1].StepKey != "second" {
		t.Errorf("steps out of order: %+v", got.Steps)
	}
	if len(got.Steps[1].DependsOn) != 1 || got.Steps[1].DependsOn[0] != "first" {
		t.Errorf("depends_on not persisted: %+v", got.Steps[1].DependsOn)
	}
}

func TestRunFlowChainsOutput(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	r := NewRunner(svc, reg)

	// Step 1 echoes the context var "seed". Step 2 echoes step 1's output via
	// {{steps.first.output}} — proving the variable bag threads between steps.
	s1, _ := svc.CreateAction(Action{Name: "s1", Kind: "echo", Spec: `{"var":"seed"}`})
	s2, _ := svc.CreateAction(Action{Name: "s2", Kind: "echo", Spec: `{"var":"steps.first.output"}`})

	f, _ := svc.CreateFlow(Flow{Name: "chain"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: s1.ID, StepKey: "first"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: s2.ID, StepKey: "second"})

	res, err := r.RunFlow(context.Background(), f.ID, TriggerManual,
		RunContext{Vars: map[string]string{"seed": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(res.Steps))
	}
	if res.Steps[1].Output != "hello" {
		t.Errorf("step 2 should echo step 1's output, got %q", res.Steps[1].Output)
	}
	// One flow run recorded.
	if runs, _ := svc.RecentRuns(5); len(runs) != 1 || runs[0].TargetKind != "flow" {
		t.Errorf("flow run not recorded: %+v", runs)
	}
}

func TestRunFlowStopsAtFailure(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	r := NewRunner(svc, reg)

	ok, _ := svc.CreateAction(Action{Name: "ok", Kind: "echo", Spec: `{"var":"x"}`})
	boom, _ := svc.CreateAction(Action{Name: "boom", Kind: "echo", Spec: `{"fail":true}`})
	after, _ := svc.CreateAction(Action{Name: "after", Kind: "echo", Spec: `{"var":"x"}`})

	f, _ := svc.CreateFlow(Flow{Name: "halts"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: ok.ID, StepKey: "a"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: boom.ID, StepKey: "b"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 2, ActionID: after.ID, StepKey: "c"})

	res, _ := r.RunFlow(context.Background(), f.ID, TriggerManual, RunContext{Vars: map[string]string{"x": "v"}})
	if res.Status != StatusError {
		t.Fatalf("expected error, got %q", res.Status)
	}
	// Pipeline stops at the failing step — the third never runs.
	if len(res.Steps) != 2 {
		t.Errorf("expected 2 steps executed (stop at failure), got %d", len(res.Steps))
	}
}

func TestHookTargetingFlow(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	r := NewRunner(svc, reg)

	// A flow of two echo steps, bound to pr.approve as a secondary hook.
	s1, _ := svc.CreateAction(Action{Name: "s1", Kind: "echo", Spec: `{"var":"x"}`})
	s2, _ := svc.CreateAction(Action{Name: "s2", Kind: "echo", Spec: `{"var":"steps.a.output"}`})
	f, _ := svc.CreateFlow(Flow{Name: "notify-flow"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: s1.ID, StepKey: "a"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: s2.ID, StepKey: "b"})
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "flow", TargetID: f.ID, Enabled: true})

	res, _ := r.FireSecondary(context.Background(), EventPRApprove,
		RunContext{RepoID: "repo-A", Vars: map[string]string{"x": "go"}})

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	// Both flow steps contributed to the chain's hook results.
	if len(res.Hooks) != 2 {
		t.Fatalf("flow's 2 steps should contribute 2 hook results, got %d", len(res.Hooks))
	}
	if res.Hooks[1].Output != "go" {
		t.Errorf("flow output chaining broken in hook context: %q", res.Hooks[1].Output)
	}
}

func TestRenderTemplateAllowsDottedVars(t *testing.T) {
	got := RenderTemplate("out={{steps.build.output}}", map[string]string{"steps.build.output": "42"})
	if got != "out=42" {
		t.Errorf("dotted var not substituted: %q", got)
	}
	// {{secret.*}} must survive RenderTemplate untouched (resolved later).
	if RenderTemplate("{{secret.tok}}", nil) != "{{secret.tok}}" {
		t.Error("secret placeholder should survive RenderTemplate")
	}
}
