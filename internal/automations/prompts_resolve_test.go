package automations

import (
	"strings"
	"testing"
)

func TestRenderSSHGuidance(t *testing.T) {
	svc := newService(t)

	// No owner/name → no guidance.
	if g := svc.RenderSSHGuidance("repo-A", ""); g != "" {
		t.Errorf("expected empty guidance for no owner, got %q", g)
	}
	// With owner/name → the built-in sentence with the remote filled.
	g := svc.RenderSSHGuidance("repo-A", "acme/widget")
	if !strings.Contains(g, "git@github.com:acme/widget.git") || !strings.Contains(g, "ssh-agent") {
		t.Errorf("guidance not rendered with remote: %q", g)
	}
	// Editing the ssh.guidance prompt changes it everywhere.
	svc.SetPromptOverride(PromptSSHGuidance, "", "PUSH VIA {{ssh_remote}}")
	g = svc.RenderSSHGuidance("repo-A", "acme/widget")
	if g != "PUSH VIA git@github.com:acme/widget.git" {
		t.Errorf("override not applied: %q", g)
	}
}

func TestPromptCatalogComplete(t *testing.T) {
	cat := PromptCatalog()
	if len(cat) < 10 {
		t.Fatalf("expected the full prompt catalog (10+), got %d", len(cat))
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
	if !strings.Contains(DefaultSSHPushGuidance, "ssh-agent") || !strings.Contains(DefaultSSHPushGuidance, "{{ssh_remote}}") {
		t.Error("DefaultSSHPushGuidance should mention the SSH remote + scoped agent")
	}
	// ssh.guidance is its own editable catalog prompt.
	if _, ok := PromptDefFor(PromptSSHGuidance); !ok {
		t.Error("ssh.guidance should be in the catalog")
	}
	// The project-start defaults must also leave the shared engineering-principles
	// slot, and engineering.principles must be its own editable catalog prompt.
	if !strings.Contains(ps.Default, "{{engineering_principles}}") {
		t.Error("project.start default should leave an {{engineering_principles}} slot")
	}
	iss, _ := PromptDefFor(PromptProjectIssue)
	if !strings.Contains(iss.Default, "{{engineering_principles}}") {
		t.Error("project.issue default should leave an {{engineering_principles}} slot")
	}
	if _, ok := PromptDefFor(PromptEngPrinciples); !ok {
		t.Error("engineering.principles should be in the catalog")
	}
	for _, want := range []string{"Root cause", "Chesterton", "linter", "stacked"} {
		if !strings.Contains(DefaultEngineeringPrinciples, want) {
			t.Errorf("DefaultEngineeringPrinciples should mention %q", want)
		}
	}
}

func TestRepoAgentsMdPrompt(t *testing.T) {
	def, ok := PromptDefFor(PromptRepoAgentsMd)
	if !ok {
		t.Fatal("repo.agents_md should be in the catalog")
	}
	// Slots the code fills must appear in the template.
	for _, slot := range []string{"{{repo}}", "{{repoId}}", "{{cache_path}}", "{{default_branch}}"} {
		if !strings.Contains(def.Default, slot) {
			t.Errorf("repo.agents_md default missing slot %q", slot)
		}
	}
	// The empirical quality bar + the user's asks must be present.
	for _, want := range []string{
		"150",                           // length bar (≤150 lines)
		"ROOT CAUSE",                    // root-cause fixes
		"Chesterton",                    // Chesterton's fence
		"linter",                        // run the linter
		"changed code",                  // scope tests to the changed code when large
		"stacked",                       // small stacked commits
		"Do NOT invent",                 // only real commands
		"corral repo set-agent-context", // save path
		"Definition of Done",
		"run the app", // figure out how to run the app
	} {
		if !strings.Contains(def.Default, want) {
			t.Errorf("repo.agents_md default should mention %q", want)
		}
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
