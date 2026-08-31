package automations

import (
	"strings"
	"testing"
)

func TestRepoSkillCRUD(t *testing.T) {
	svc := newService(t)

	sk, err := svc.CreateRepoSkill("repo-1", "review-rules", "---\nname: review-rules\n---\nBe strict.")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "review-rules" || sk.RepoID != "repo-1" || !strings.Contains(sk.Content, "Be strict") {
		t.Fatalf("created skill wrong: %+v", sk)
	}

	list, _ := svc.ListRepoSkills("repo-1")
	if len(list) != 1 || list[0].ID != sk.ID {
		t.Fatalf("list wrong: %+v", list)
	}
	// A different repo doesn't see it.
	if other, _ := svc.ListRepoSkills("repo-2"); len(other) != 0 {
		t.Errorf("repo-2 should not see repo-1's skill, got %+v", other)
	}

	up, err := svc.UpdateRepoSkill(sk.ID, "review-rules", "updated content")
	if err != nil {
		t.Fatal(err)
	}
	if up.Content != "updated content" {
		t.Errorf("update content wrong: %+v", up)
	}

	if err := svc.DeleteRepoSkill(sk.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.ListRepoSkills("repo-1"); len(list) != 0 {
		t.Errorf("expected empty after delete")
	}
}

func TestRepoSkillNameValidation(t *testing.T) {
	svc := newService(t)
	bad := []string{"", "has spaces", "has/slash", "with.dot", strings.Repeat("x", 65)}
	for _, n := range bad {
		if _, err := svc.CreateRepoSkill("repo-1", n, "x"); err == nil {
			t.Errorf("expected invalid name %q to be rejected", n)
		}
	}
	for _, n := range []string{"review-rules", "test_skill", "Skill1"} {
		if _, err := svc.CreateRepoSkill("repo-1", n, "x"); err != nil {
			t.Errorf("valid name %q rejected: %v", n, err)
		}
	}
}

// names returns the sorted skill names in a resolved set, for terse assertions.
func names(sks []RepoSkill) []string {
	out := make([]string, len(sks))
	for i, s := range sks {
		out[i] = s.Name
	}
	return out
}

func has(sks []RepoSkill, name string) bool {
	for _, s := range sks {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestGlobalSkillCRUDAndList(t *testing.T) {
	svc := newService(t)

	g, err := svc.CreateGlobalSkill("house-style", "Be terse.", true)
	if err != nil {
		t.Fatal(err)
	}
	if g.Scope != ScopeGlobal || !g.AutoAll || g.RepoID != "" {
		t.Fatalf("global skill wrong: %+v", g)
	}
	list, _ := svc.ListGlobalSkills()
	if len(list) != 1 || list[0].ID != g.ID {
		t.Fatalf("global list wrong: %+v", list)
	}
	// It must NOT show up in a repo's own skills list.
	if own, _ := svc.ListRepoSkills("repo-1"); len(own) != 0 {
		t.Errorf("ListRepoSkills should exclude globals, got %+v", own)
	}

	up, err := svc.UpdateGlobalSkill(g.ID, "house-style", "Be very terse.", false)
	if err != nil {
		t.Fatal(err)
	}
	if up.AutoAll || up.Content != "Be very terse." {
		t.Errorf("update wrong: %+v", up)
	}

	if err := svc.DeleteGlobalSkill(g.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.ListGlobalSkills(); len(l) != 0 {
		t.Errorf("expected no globals after delete")
	}
}

// TestEffectiveSkillsPrecedence exercises all six (autoAll × pref) combinations.
func TestEffectiveSkillsPrecedence(t *testing.T) {
	svc := newService(t)
	// autoAll=true skill, and autoAll=false skill.
	if _, err := svc.CreateGlobalSkill("auto-on", "x", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGlobalSkill("auto-off", "x", false); err != nil {
		t.Fatal(err)
	}

	repo := "repo-1"

	// Row: autoAll=true, pref=unset → injected.
	// Row: autoAll=false, pref=unset → not injected.
	eff, err := svc.EffectiveSkills(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !has(eff, "auto-on") || has(eff, "auto-off") {
		t.Fatalf("unset prefs wrong: %v", names(eff))
	}

	// Row: autoAll=false, pref=enabled → injected (repo opted in).
	// Row: autoAll=true, pref=disabled → not injected (repo opted out).
	if err := svc.SetRepoSkillEnabled(repo, "auto-off", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRepoSkillEnabled(repo, "auto-on", false); err != nil {
		t.Fatal(err)
	}
	eff, _ = svc.EffectiveSkills(repo)
	if !has(eff, "auto-off") || has(eff, "auto-on") {
		t.Fatalf("explicit prefs wrong: %v", names(eff))
	}

	// Row: autoAll=false, pref=disabled → not injected (redundant but explicit).
	// Row: autoAll=true, pref=enabled → injected.
	if err := svc.SetRepoSkillEnabled(repo, "auto-off", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRepoSkillEnabled(repo, "auto-on", true); err != nil {
		t.Fatal(err)
	}
	eff, _ = svc.EffectiveSkills(repo)
	if has(eff, "auto-off") || !has(eff, "auto-on") {
		t.Fatalf("flipped prefs wrong: %v", names(eff))
	}

	// Clearing a pref reverts to the global's AutoAll default.
	if err := svc.ClearRepoSkillPref(repo, "auto-on"); err != nil {
		t.Fatal(err)
	}
	eff, _ = svc.EffectiveSkills(repo)
	if !has(eff, "auto-on") { // auto-on's AutoAll=true default returns
		t.Fatalf("clear pref should revert to autoAll: %v", names(eff))
	}

	// A different repo, with no prefs, sees only the auto-add globals.
	other, _ := svc.EffectiveSkills("repo-2")
	if !has(other, "auto-on") || has(other, "auto-off") {
		t.Fatalf("other repo inherits AutoAll defaults only: %v", names(other))
	}
}

// A repo skill overrides a global skill of the same name (content wins).
func TestEffectiveSkillsRepoOverridesGlobalByName(t *testing.T) {
	svc := newService(t)
	if _, err := svc.CreateGlobalSkill("shared", "GLOBAL body", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRepoSkill("repo-1", "shared", "REPO body"); err != nil {
		t.Fatal(err)
	}
	eff, err := svc.EffectiveSkills("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	var shared *RepoSkill
	for i := range eff {
		if eff[i].Name == "shared" {
			shared = &eff[i]
		}
	}
	if shared == nil {
		t.Fatal("expected 'shared' in effective set")
	}
	if shared.Content != "REPO body" || shared.Scope != ScopeRepo {
		t.Fatalf("repo skill should win over global: %+v", shared)
	}
	// Only ONE 'shared' — the map dedupes by name.
	n := 0
	for _, s := range eff {
		if s.Name == "shared" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one 'shared', got %d", n)
	}
}

func TestPromoteSkillToGlobal(t *testing.T) {
	svc := newService(t)
	r, err := svc.CreateRepoSkill("repo-1", "promo", "shared knowledge")
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.PromoteSkillToGlobal(r.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != r.ID {
		t.Errorf("promote should preserve id: %d != %d", g.ID, r.ID)
	}
	if g.Scope != ScopeGlobal || g.RepoID != "" || !g.AutoAll || g.Content != "shared knowledge" {
		t.Fatalf("promoted skill wrong: %+v", g)
	}
	// It's gone from the repo's own list, present in the global catalog.
	if own, _ := svc.ListRepoSkills("repo-1"); len(own) != 0 {
		t.Errorf("promoted skill should leave repo's own list: %+v", own)
	}
	if gl, _ := svc.ListGlobalSkills(); len(gl) != 1 || gl[0].ID != r.ID {
		t.Errorf("promoted skill should be in global catalog: %+v", gl)
	}
	// Promoting a non-repo (already global) skill is rejected.
	if _, err := svc.PromoteSkillToGlobal(g.ID, false); err == nil {
		t.Errorf("promoting an already-global skill should error")
	}
}

func TestRepoAgentContext(t *testing.T) {
	svc := newService(t)

	// None initially.
	if c, _ := svc.RepoAgentContext("repo-1"); c != "" {
		t.Errorf("expected empty context, got %q", c)
	}

	// Set.
	if err := svc.SetRepoAgentContext("repo-1", "# Rules\nUse tabs."); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.RepoAgentContext("repo-1"); c != "# Rules\nUse tabs." {
		t.Errorf("context not saved: %q", c)
	}

	// Upsert (one per repo — not a second row).
	if err := svc.SetRepoAgentContext("repo-1", "# Rules\nUse spaces."); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.RepoAgentContext("repo-1"); c != "# Rules\nUse spaces." {
		t.Errorf("context not updated: %q", c)
	}
	// Still only one agent_context action.
	actions, _ := svc.ListActions("repo-1")
	n := 0
	for _, a := range actions {
		if a.Kind == KindAgentContext {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected exactly 1 agent_context action, got %d", n)
	}

	// Empty clears it.
	if err := svc.SetRepoAgentContext("repo-1", "  "); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.RepoAgentContext("repo-1"); c != "" {
		t.Errorf("empty set should clear context, got %q", c)
	}
}
