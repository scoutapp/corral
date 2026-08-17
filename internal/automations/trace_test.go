package automations

import (
	"context"
	"testing"
)

// recordingTracer captures span opens/closes so we can assert the runner emits a
// flow span + a child span per step.
type recordingTracer struct {
	opened []string // event names, in open order
	closed int
	errs   int
}

func (rt *recordingTracer) StartSpan(ctx context.Context, _, event, _ string, _ map[string]any) (context.Context, func(error)) {
	rt.opened = append(rt.opened, event)
	return ctx, func(err error) {
		rt.closed++
		if err != nil {
			rt.errs++
		}
	}
}

func TestRunFlowEmitsSpans(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	rt := &recordingTracer{}
	r := NewRunner(svc, reg).WithTracer(rt)

	a1, _ := svc.CreateAction(Action{Name: "a1", Kind: "echo", Spec: `{"var":"seed"}`})
	a2, _ := svc.CreateAction(Action{Name: "a2", Kind: "echo", Spec: `{"var":"seed"}`})
	f, _ := svc.CreateFlow(Flow{Name: "traced"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: a1.ID, StepKey: "one"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: a2.ID, StepKey: "two"})

	res, err := r.RunFlow(context.Background(), f.ID, TriggerManual, RunContext{Vars: map[string]string{"seed": "x"}})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("run: %v status=%s", err, res.Status)
	}

	// One flow span + two step spans, all closed, none errored.
	if len(rt.opened) != 3 {
		t.Fatalf("expected 3 spans opened, got %v", rt.opened)
	}
	if rt.opened[0] != "flow.run" {
		t.Errorf("first span = %q, want flow.run", rt.opened[0])
	}
	if rt.opened[1] != "flow.step" || rt.opened[2] != "flow.step" {
		t.Errorf("step spans = %v, want two flow.step", rt.opened[1:])
	}
	if rt.closed != 3 {
		t.Errorf("expected 3 spans closed, got %d", rt.closed)
	}
	if rt.errs != 0 {
		t.Errorf("expected no errored spans, got %d", rt.errs)
	}
}

func TestRunFlowSpanErrorsOnStepFailure(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	reg.Register("boom", boomExecutor{})
	rt := &recordingTracer{}
	r := NewRunner(svc, reg).WithTracer(rt)

	ok, _ := svc.CreateAction(Action{Name: "ok", Kind: "echo", Spec: `{"var":"x"}`})
	bad, _ := svc.CreateAction(Action{Name: "bad", Kind: "boom", Spec: `{}`})
	f, _ := svc.CreateFlow(Flow{Name: "fails"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: ok.ID, StepKey: "ok"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: bad.ID, StepKey: "bad"})

	r.RunFlow(context.Background(), f.ID, TriggerManual, RunContext{})

	// flow.run + 2 step spans opened; the failing step span AND the flow span end
	// errored → 2 errored ends.
	if rt.errs != 2 {
		t.Errorf("expected 2 errored spans (failing step + flow), got %d of %d opened", rt.errs, len(rt.opened))
	}
	if rt.closed != len(rt.opened) {
		t.Errorf("every opened span should be closed: opened=%d closed=%d", len(rt.opened), rt.closed)
	}
}

// boomExecutor always fails, for exercising the error path.
type boomExecutor struct{}

func (boomExecutor) Execute(context.Context, Action, RunContext) StepResult {
	return StepResult{Status: StatusError, Err: "boom"}
}
