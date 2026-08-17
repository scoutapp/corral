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
