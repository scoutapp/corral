package automations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// echoExecutor is a trivial test executor: it echoes a context var named by the
// action's spec {"var":"..."} back as output, or fails if the action's spec
// says {"fail":true}.
type echoExecutor struct{}

func (echoExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec struct {
		Var  string `json:"var"`
		Fail bool   `json:"fail"`
	}
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	if spec.Fail {
		return StepResult{Status: StatusError, Err: "asked to fail"}
	}
	return StepResult{Status: StatusOK, Output: rc.Var(spec.Var)}
}

func newRunner(t *testing.T) (*Runner, *Service) {
	t.Helper()
	svc := newService(t)
	reg := NewRegistry()
	reg.Register("echo", echoExecutor{})
	return NewRunner(svc, reg), svc
}

func TestRunActionRecordsSuccess(t *testing.T) {
	r, svc := newRunner(t)
	a, err := svc.CreateAction(Action{Name: "echo-pr", Kind: "echo", Spec: `{"var":"pr_url"}`})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.RunAction(context.Background(), a.ID, TriggerManual, RunContext{
		Event:  EventPRApprove,
		RepoID: "repo-A",
		Vars:   map[string]string{"pr_url": "https://x/pr/7"},
	})
	if err != nil {
		t.Fatalf("RunAction: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if res.Output != "https://x/pr/7" {
		t.Errorf("output not from context: %q", res.Output)
	}
	if res.Name != "echo-pr" || res.Kind != "echo" {
		t.Errorf("result not labeled with action: %+v", res)
	}

	// A run row was recorded and finalized.
	runs, err := svc.RecentRuns(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	run := runs[0]
	if run.Status != StatusOK || run.FinishedAt == "" {
		t.Errorf("run not finalized: %+v", run)
	}
	if run.Event != EventPRApprove || run.TargetID != a.ID {
		t.Errorf("run metadata wrong: %+v", run)
	}
	if !strings.Contains(run.Context, "repo-A") {
		t.Errorf("context not persisted: %s", run.Context)
	}
	if !strings.Contains(run.Steps, "https://x/pr/7") {
		t.Errorf("step output not persisted: %s", run.Steps)
	}
}

func TestRunActionRecordsFailure(t *testing.T) {
	r, svc := newRunner(t)
	a, _ := svc.CreateAction(Action{Name: "boom", Kind: "echo", Spec: `{"fail":true}`})

	res, err := r.RunAction(context.Background(), a.ID, TriggerAPI, RunContext{})
	if err != nil {
		t.Fatalf("RunAction should not return engine error for action failure: %v", err)
	}
	if res.Status != StatusError {
		t.Fatalf("expected error status, got %q", res.Status)
	}
	runs, _ := svc.RecentRuns(1)
	if len(runs) != 1 || runs[0].Status != StatusError {
		t.Errorf("failure not recorded: %+v", runs)
	}
}

func TestRunActionMissingExecutor(t *testing.T) {
	r, svc := newRunner(t)
	// A kind with no registered executor still records a run (as error).
	a, _ := svc.CreateAction(Action{Name: "unknown", Kind: "nope"})
	res, err := r.RunAction(context.Background(), a.ID, TriggerManual, RunContext{})
	if err != nil {
		t.Fatalf("RunAction: %v", err)
	}
	if res.Status != StatusError || !strings.Contains(res.Err, "no executor") {
		t.Errorf("expected missing-executor error, got %+v", res)
	}
	if runs, _ := svc.RecentRuns(1); len(runs) != 1 {
		t.Error("missing-executor run should still be recorded")
	}
}
