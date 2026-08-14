package automations

import (
	"context"
	"testing"
)

// fakeProvider records the calls made to it instead of shelling out to gh.
type fakeProvider struct {
	calls    []string
	lastBody string
	lastTgt  PRTarget
	fail     error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Approve(_ context.Context, t PRTarget, body string) error {
	f.calls = append(f.calls, "approve")
	f.lastBody, f.lastTgt = body, t
	return f.fail
}
func (f *fakeProvider) RequestChanges(_ context.Context, t PRTarget, body string) error {
	f.calls = append(f.calls, "request-changes")
	f.lastBody, f.lastTgt = body, t
	return f.fail
}
func (f *fakeProvider) Comment(_ context.Context, t PRTarget, body string) error {
	f.calls = append(f.calls, "comment")
	f.lastBody, f.lastTgt = body, t
	return f.fail
}
func (f *fakeProvider) Merge(_ context.Context, t PRTarget, method string) error {
	f.calls = append(f.calls, "merge:"+method)
	f.lastTgt = t
	return f.fail
}

func prCtx() RunContext {
	return RunContext{
		Event:  EventPRApprove,
		RepoID: "repo-A",
		Vars: map[string]string{
			"owner_name": "acme/widget",
			"pr_number":  "42",
			"head_sha":   "deadbeef",
		},
	}
}

func TestCapabilityDispatch(t *testing.T) {
	fp := &fakeProvider{}
	e := NewCapabilityExecutor(fp)

	res := e.Execute(context.Background(),
		Action{Name: "approve", Kind: KindCapability, Spec: `{"capability":"pr-approve","body":"LGTM"}`},
		prCtx())

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if len(fp.calls) != 1 || fp.calls[0] != "approve" {
		t.Fatalf("expected approve call, got %v", fp.calls)
	}
	if fp.lastBody != "LGTM" {
		t.Errorf("spec body not used: %q", fp.lastBody)
	}
	if fp.lastTgt.OwnerName != "acme/widget" || fp.lastTgt.Number != 42 || fp.lastTgt.HeadSHA != "deadbeef" {
		t.Errorf("target not resolved from context: %+v", fp.lastTgt)
	}
}

func TestCapabilityContextBodyOverridesSpec(t *testing.T) {
	fp := &fakeProvider{}
	e := NewCapabilityExecutor(fp)
	rc := prCtx()
	rc.Vars["body"] = "actual review text"

	e.Execute(context.Background(),
		Action{Kind: KindCapability, Spec: `{"capability":"pr-comment","body":"fallback"}`}, rc)

	if fp.lastBody != "actual review text" {
		t.Errorf("context body should override spec, got %q", fp.lastBody)
	}
}

func TestCapabilityMergeMethodFromContext(t *testing.T) {
	fp := &fakeProvider{}
	e := NewCapabilityExecutor(fp)
	rc := prCtx()
	rc.Vars["method"] = "rebase"

	e.Execute(context.Background(),
		Action{Kind: KindCapability, Spec: `{"capability":"pr-merge","method":"squash"}`}, rc)

	if len(fp.calls) != 1 || fp.calls[0] != "merge:rebase" {
		t.Errorf("context method should override spec, got %v", fp.calls)
	}
}

func TestCapabilityMissingTarget(t *testing.T) {
	e := NewCapabilityExecutor(&fakeProvider{})
	res := e.Execute(context.Background(),
		Action{Kind: KindCapability, Spec: `{"capability":"pr-approve"}`},
		RunContext{}) // no owner_name / pr_number
	if res.Status != StatusError {
		t.Fatalf("expected error for missing target, got %q", res.Status)
	}
}

func TestCapabilityUnknown(t *testing.T) {
	e := NewCapabilityExecutor(&fakeProvider{})
	res := e.Execute(context.Background(),
		Action{Kind: KindCapability, Spec: `{"capability":"pr-teleport"}`}, prCtx())
	if res.Status != StatusError {
		t.Fatalf("expected error for unknown capability, got %q", res.Status)
	}
}

// End-to-end through the runner + DefaultRegistry shape (using a fake provider
// registered in place of the GitHub one).
func TestCapabilityThroughRunner(t *testing.T) {
	svc := newService(t)
	fp := &fakeProvider{}
	reg := NewRegistry()
	reg.Register(KindCapability, NewCapabilityExecutor(fp))
	r := NewRunner(svc, reg)

	a, _ := svc.CreateAction(Action{Name: "approve", Kind: KindCapability, Spec: `{"capability":"pr-approve"}`})
	res, err := r.RunAction(context.Background(), a.ID, TriggerHook, prCtx())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if len(fp.calls) != 1 {
		t.Errorf("provider not called through runner: %v", fp.calls)
	}
	if runs, _ := svc.RecentRuns(1); len(runs) != 1 || runs[0].Status != StatusOK {
		t.Error("capability run not recorded ok")
	}
}
