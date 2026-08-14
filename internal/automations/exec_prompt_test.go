package automations

import (
	"context"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	vars := map[string]string{"repo": "acme/widget", "branch": "feat/x"}
	cases := []struct{ in, want string }{
		{"on {{repo}} @ {{branch}}", "on acme/widget @ feat/x"},
		{"spaces {{ repo }} ok", "spaces acme/widget ok"},
		{"unknown {{missing}} blanked", "unknown  blanked"},
		{"no vars here", "no vars here"},
	}
	for _, c := range cases {
		if got := RenderTemplate(c.in, vars); got != c.want {
			t.Errorf("RenderTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPromptExecutor(t *testing.T) {
	res := PromptExecutor{}.Execute(context.Background(),
		Action{Kind: KindClaudePrompt, Spec: `{"template":"Verify PR {{pr_number}} on {{repo}}"}`},
		RunContext{Vars: map[string]string{"pr_number": "42", "repo": "acme/widget"}})
	if res.Status != StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Err)
	}
	if res.Output != "Verify PR 42 on acme/widget" {
		t.Errorf("bad render: %q", res.Output)
	}
}

func TestResolveProjectStartPromptPrecedence(t *testing.T) {
	svc := newService(t)

	// No config anywhere → built-in default.
	tmpl, src, err := svc.ResolveProjectStartPrompt("repo-A")
	if err != nil {
		t.Fatal(err)
	}
	if src != "default" || tmpl != DefaultProjectStartPrompt {
		t.Fatalf("expected built-in default, got src=%q", src)
	}

	// Add a GLOBAL prompt bound to project.start.
	g, _ := svc.CreateAction(Action{Name: "global-prompt", Kind: KindClaudePrompt, Spec: `{"template":"GLOBAL {{repo}}"}`})
	mustHook(t, svc, Hook{Event: EventProjectStart, TargetKind: "action", TargetID: g.ID, Enabled: true})

	tmpl, src, _ = svc.ResolveProjectStartPrompt("repo-A")
	if src != "global" || tmpl != "GLOBAL {{repo}}" {
		t.Fatalf("expected global, got src=%q tmpl=%q", src, tmpl)
	}

	// Add a REPO-scoped prompt for repo-A → wins over global.
	rp, _ := svc.CreateAction(Action{Name: "repo-prompt", Kind: KindClaudePrompt, Scope: ScopeRepo, RepoID: "repo-A", Spec: `{"template":"REPO {{repo}}"}`})
	mustHook(t, svc, Hook{Event: EventProjectStart, Scope: ScopeRepo, RepoID: "repo-A", TargetKind: "action", TargetID: rp.ID, Enabled: true})

	tmpl, src, _ = svc.ResolveProjectStartPrompt("repo-A")
	if src != "repo" || tmpl != "REPO {{repo}}" {
		t.Fatalf("expected repo override, got src=%q tmpl=%q", src, tmpl)
	}

	// A different repo still gets the global one (not repo-A's override).
	tmpl, src, _ = svc.ResolveProjectStartPrompt("repo-B")
	if src != "global" {
		t.Fatalf("repo-B should fall back to global, got src=%q", src)
	}
}
