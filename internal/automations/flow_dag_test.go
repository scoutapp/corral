package automations

import (
	"context"
	"strings"
	"testing"
)

func keys(steps []FlowStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.StepKey
	}
	return out
}

func TestOrderStepsLinearIsPositionOrder(t *testing.T) {
	steps := []FlowStep{
		{ID: 1, Position: 0, StepKey: "a"},
		{ID: 2, Position: 1, StepKey: "b"},
		{ID: 3, Position: 2, StepKey: "c"},
	}
	got, err := orderSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys(got), ",") != "a,b,c" {
		t.Errorf("linear order = %v, want a,b,c", keys(got))
	}
}

func TestOrderStepsHonorsDependsOn(t *testing.T) {
	// Positions say c,b,a — but deps say a→b→c, so dependency order wins.
	steps := []FlowStep{
		{ID: 1, Position: 0, StepKey: "c", DependsOn: []string{"b"}},
		{ID: 2, Position: 1, StepKey: "b", DependsOn: []string{"a"}},
		{ID: 3, Position: 2, StepKey: "a"},
	}
	got, err := orderSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys(got), ",") != "a,b,c" {
		t.Errorf("dependency order = %v, want a,b,c", keys(got))
	}
}

func TestOrderStepsPositionTiebreak(t *testing.T) {
	// b and c both depend on a; among ready steps, lower position runs first.
	steps := []FlowStep{
		{ID: 1, Position: 0, StepKey: "a"},
		{ID: 2, Position: 2, StepKey: "c", DependsOn: []string{"a"}},
		{ID: 3, Position: 1, StepKey: "b", DependsOn: []string{"a"}},
	}
	got, _ := orderSteps(steps)
	if strings.Join(keys(got), ",") != "a,b,c" {
		t.Errorf("tiebreak order = %v, want a,b,c (a, then lower-position b, then c)", keys(got))
	}
}

func TestOrderStepsCycle(t *testing.T) {
	steps := []FlowStep{
		{ID: 1, Position: 0, StepKey: "a", DependsOn: []string{"b"}},
		{ID: 2, Position: 1, StepKey: "b", DependsOn: []string{"a"}},
	}
	if _, err := orderSteps(steps); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected a cycle error, got %v", err)
	}
}

func TestOrderStepsUnknownDependency(t *testing.T) {
	steps := []FlowStep{
		{ID: 1, Position: 0, StepKey: "a", DependsOn: []string{"ghost"}},
	}
	if _, err := orderSteps(steps); err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("expected an unknown-step error, got %v", err)
	}
}

// Integration: a flow whose steps are stored in one position order but wired with
// depends_on runs in dependency order, and output threads along the real order.
func TestRunFlowExecutesInDependencyOrder(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	r := NewRunner(svc, reg)

	// seed → "hello"; mid echoes seed's output; last echoes mid's output.
	// But we ADD them in reverse position so only depends_on gives the right order.
	seed, _ := svc.CreateAction(Action{Name: "seed", Kind: "echo", Spec: `{"var":"seed"}`})
	mid, _ := svc.CreateAction(Action{Name: "mid", Kind: "echo", Spec: `{"var":"steps.seed.output"}`})
	last, _ := svc.CreateAction(Action{Name: "last", Kind: "echo", Spec: `{"var":"steps.mid.output"}`})

	f, _ := svc.CreateFlow(Flow{Name: "dag"})
	// Positions deliberately do NOT match dependency order.
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: last.ID, StepKey: "last", DependsOn: []string{"mid"}})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: mid.ID, StepKey: "mid", DependsOn: []string{"seed"}})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 2, ActionID: seed.ID, StepKey: "seed"})

	res, err := r.RunFlow(context.Background(), f.ID, TriggerManual,
		RunContext{Vars: map[string]string{"seed": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Steps)
	}
	// Executed order is seed, mid, last — so the last result echoes "hello".
	if len(res.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(res.Steps))
	}
	if res.Steps[2].Output != "hello" {
		t.Errorf("final step output = %q, want hello (proves dep-ordered threading)", res.Steps[2].Output)
	}
}

func TestRunFlowCycleErrors(t *testing.T) {
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	r := NewRunner(svc, reg)
	a, _ := svc.CreateAction(Action{Name: "a", Kind: "echo", Spec: `{"var":"x"}`})
	b, _ := svc.CreateAction(Action{Name: "b", Kind: "echo", Spec: `{"var":"x"}`})
	f, _ := svc.CreateFlow(Flow{Name: "cyclic"})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 0, ActionID: a.ID, StepKey: "a", DependsOn: []string{"b"}})
	svc.AddStep(FlowStep{FlowID: f.ID, Position: 1, ActionID: b.ID, StepKey: "b", DependsOn: []string{"a"}})

	res, _ := r.RunFlow(context.Background(), f.ID, TriggerManual, RunContext{})
	if res.Status != StatusError {
		t.Errorf("cyclic flow should error, got %q", res.Status)
	}
}
