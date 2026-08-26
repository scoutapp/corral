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

func TestBashExecutorLoginShell(t *testing.T) {
	// With a login shell set, the script runs via `<shell> -lc` and still sees
	// the injected CORRAL_ env. /bin/bash is always present.
	e := BashExecutor{LoginShell: "/bin/bash"}
	res := e.Execute(context.Background(),
		Action{Kind: KindBash, Spec: `{"script":"echo hi=$CORRAL_PR_NUMBER"}`},
		RunContext{Vars: map[string]string{"pr_number": "9"}})
	if res.Status != StatusOK || res.Output != "hi=9" {
		t.Fatalf("login-shell run wrong: %+v", res)
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

// fakeScriptEnv injects a fixed env for a specific action id.
type fakeScriptEnv struct {
	id  int64
	env []string
}

func (f fakeScriptEnv) ScriptEnv(id int64) []string {
	if id == f.id {
		return f.env
	}
	return nil
}

// TestBashExecutorInjectsScriptSecrets: the resolver's VAR=value entries reach
// the script's process env (so e.g. FRESHDESK_API_KEY is available), and win over
// ambient env.
func TestBashExecutorInjectsScriptSecrets(t *testing.T) {
	e := BashExecutor{ScriptEnv: fakeScriptEnv{id: 3, env: []string{"FRESHDESK_API_KEY=sk-fd-xyz", "FRESHDESK_DOMAIN=scoutapm"}}}
	res := e.Execute(context.Background(),
		Action{ID: 3, Kind: KindBash, Spec: `{"script":"echo key=$FRESHDESK_API_KEY dom=$FRESHDESK_DOMAIN"}`},
		RunContext{})
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if res.Output != "key=sk-fd-xyz dom=scoutapm" {
		t.Errorf("script secrets not injected: %q", res.Output)
	}
}

// A different action id gets no injected secrets (namespacing).
func TestBashExecutorScriptSecretsScopedToAction(t *testing.T) {
	e := BashExecutor{ScriptEnv: fakeScriptEnv{id: 3, env: []string{"FRESHDESK_API_KEY=sk-fd-xyz"}}}
	res := e.Execute(context.Background(),
		Action{ID: 99, Kind: KindBash, Spec: `{"script":"echo key=${FRESHDESK_API_KEY:-none}"}`},
		RunContext{})
	if res.Output != "key=none" {
		t.Errorf("action 99 should not get action 3's secret, got %q", res.Output)
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
