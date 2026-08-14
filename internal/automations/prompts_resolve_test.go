package automations

import (
	"strings"
	"testing"
)

func TestPromptCatalogComplete(t *testing.T) {
	cat := PromptCatalog()
	if len(cat) < 9 {
		t.Fatalf("expected the full prompt catalog, got %d", len(cat))
	}
	for _, p := range cat {
		if p.Key == "" || p.Name == "" || p.UsedWhen == "" || p.Default == "" {
			t.Errorf("incomplete catalog entry: %+v", p)
		}
		if _, ok := PromptDefFor(p.Key); !ok {
			t.Errorf("PromptDefFor(%q) not found", p.Key)
		}
	}
	// The project-start defaults must leave a slot for the SSH push guidance, and
	// the guidance sentence itself must mention the scoped agent + SSH remote.
	ps, _ := PromptDefFor(PromptProjectStart)
	if !strings.Contains(ps.Default, "{{ssh_guidance}}") {
		t.Error("project.start default should leave an {{ssh_guidance}} slot")
	}
	if !strings.Contains(SSHPushGuidance, "ssh-agent") || !strings.Contains(SSHPushGuidance, "{{ssh_remote}}") {
		t.Error("SSHPushGuidance should mention the SSH remote + scoped agent")
	}
}

func TestResolvePromptThreeLevels(t *testing.T) {
	svc := newService(t)
	key := PromptPRVerify
	def, _ := PromptDefFor(key)

	// 1. Nothing set → built-in default.
	tmpl, src := svc.ResolvePrompt(key, "repo-A")
	if src != "default" || tmpl != def.Default {
		t.Fatalf("expected default, got src=%q", src)
	}

	// 2. Global override → global (for any repo).
	if _, err := svc.SetPromptOverride(key, "", "GLOBAL {{pr_number}}"); err != nil {
		t.Fatal(err)
	}
	tmpl, src = svc.ResolvePrompt(key, "repo-A")
	if src != "global" || tmpl != "GLOBAL {{pr_number}}" {
		t.Fatalf("expected global, got src=%q tmpl=%q", src, tmpl)
	}
	// A different repo also sees global.
	if _, src := svc.ResolvePrompt(key, "repo-B"); src != "global" {
		t.Errorf("repo-B should see global, got %q", src)
	}

	// 3. Repo override for repo-A → repo wins there, global elsewhere.
	if _, err := svc.SetPromptOverride(key, "repo-A", "REPO {{pr_number}}"); err != nil {
		t.Fatal(err)
	}
	tmpl, src = svc.ResolvePrompt(key, "repo-A")
	if src != "repo" || tmpl != "REPO {{pr_number}}" {
		t.Fatalf("expected repo override, got src=%q tmpl=%q", src, tmpl)
	}
	if _, src := svc.ResolvePrompt(key, "repo-B"); src != "global" {
		t.Errorf("repo-B should still see global, got %q", src)
	}

	// Clearing the repo override falls back to global.
	if err := svc.ClearPromptOverride(key, "repo-A"); err != nil {
		t.Fatal(err)
	}
	if _, src := svc.ResolvePrompt(key, "repo-A"); src != "global" {
		t.Errorf("after repo clear, repo-A should see global, got %q", src)
	}
	// Clearing global falls back to default.
	if err := svc.ClearPromptOverride(key, ""); err != nil {
		t.Fatal(err)
	}
	if _, src := svc.ResolvePrompt(key, "repo-A"); src != "default" {
		t.Errorf("after global clear, should see default, got %q", src)
	}
}

func TestRenderPromptFillsSlots(t *testing.T) {
	svc := newService(t)
	svc.SetPromptOverride(PromptPRVerify, "", "Verify #{{pr_number}} at {{pr_url}}")
	got := svc.RenderPrompt(PromptPRVerify, "", map[string]string{"pr_number": "42", "pr_url": "http://x/42"})
	if got != "Verify #42 at http://x/42" {
		t.Errorf("render wrong: %q", got)
	}
	// Unknown key → empty.
	if svc.RenderPrompt("nope.key", "", nil) != "" {
		t.Error("unknown key should render empty")
	}
}

func TestSetPromptOverrideUpdatesInPlace(t *testing.T) {
	svc := newService(t)
	svc.SetPromptOverride(PromptRisk, "", "v1")
	svc.SetPromptOverride(PromptRisk, "", "v2")
	// Only one global override action should exist (updated, not duplicated).
	actions, _ := svc.ListActions("")
	n := 0
	for _, a := range actions {
		if a.Name == promptActionName(PromptRisk) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected 1 override action, got %d", n)
	}
	if tmpl, _ := svc.ResolvePrompt(PromptRisk, ""); tmpl != "v2" {
		t.Errorf("expected latest override, got %q", tmpl)
	}
}
