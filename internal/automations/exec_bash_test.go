package automations

import (
	"context"
	"strings"
	"testing"
)

func TestBashExecutorEnvAndOutput(t *testing.T) {
	// Script echoes context env vars back; asserts the CORRAL_ mapping.
	res := BashExecutor{}.Execute(context.Background(),
		Action{Kind: KindBash, Spec: `{"script":"echo pr=$CORRAL_PR_NUMBER repo=$CORRAL_REPO_ID event=$CORRAL_EVENT"}`},
		RunContext{Event: EventPRApprove, RepoID: "repo-A", Vars: map[string]string{"pr_number": "42"}})

	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	want := "pr=42 repo=repo-A event=pr.approve"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestBashExecutorNonZeroExit(t *testing.T) {
	res := BashExecutor{}.Execute(context.Background(),
		Action{Kind: KindBash, Spec: `{"script":"echo failing; exit 3"}`}, RunContext{})
	if res.Status != StatusError {
		t.Fatalf("expected error on non-zero exit, got %q", res.Status)
	}
	if !strings.Contains(res.Output, "failing") {
		t.Errorf("output should be captured even on failure: %q", res.Output)
	}
}

func TestBashExecutorEmptyScript(t *testing.T) {
	res := BashExecutor{}.Execute(context.Background(),
		Action{Kind: KindBash, Spec: `{"script":"  "}`}, RunContext{})
	if res.Status != StatusError {
		t.Fatalf("expected error for empty script, got %q", res.Status)
	}
}

func TestEnvKeyMapping(t *testing.T) {
	cases := map[string]string{
		"pr_number":  "PR_NUMBER",
		"owner-name": "OWNER_NAME",
		"URL":        "URL",
		"a.b":        "A_B",
	}
	for in, want := range cases {
		if got := envKey(in); got != want {
			t.Errorf("envKey(%q) = %q, want %q", in, got, want)
		}
	}
}
