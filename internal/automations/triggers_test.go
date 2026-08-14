package automations

import "testing"

func TestTriggersCatalog(t *testing.T) {
	ts := Triggers()
	if len(ts) == 0 {
		t.Fatal("expected a non-empty trigger catalog")
	}
	// Every trigger maps to a known event and has a title.
	for _, tr := range ts {
		if tr.Title == "" || tr.Event == "" {
			t.Errorf("trigger missing title/event: %+v", tr)
		}
		if _, ok := TriggerFor(tr.Event); !ok {
			t.Errorf("TriggerFor(%q) not found", tr.Event)
		}
	}
	// Approve has a built-in step; enter does not.
	ap, _ := TriggerFor(EventPRApprove)
	if ap.Builtin == "" {
		t.Error("approve should have a built-in step")
	}
	en, _ := TriggerFor(EventPREnter)
	if en.Builtin != "" {
		t.Error("pr.enter should have no built-in step")
	}
}

func TestEnsureProjectStartPromptIdempotent(t *testing.T) {
	svc := newService(t)

	a1, err := svc.EnsureProjectStartPrompt()
	if err != nil {
		t.Fatalf("EnsureProjectStartPrompt: %v", err)
	}
	if a1.Kind != KindClaudePrompt || a1.Name != ProjectStartPromptName {
		t.Fatalf("unexpected seeded action: %+v", a1)
	}

	// It's bound to project.start (so ResolveProjectStartPrompt sees it as global).
	tmpl, source, _ := svc.ResolveProjectStartPrompt("")
	if source != "global" || tmpl != DefaultProjectStartPrompt {
		t.Errorf("seeded prompt not resolved as global default: src=%q", source)
	}

	// A second call returns the SAME action (no duplicate, no second hook).
	a2, _ := svc.EnsureProjectStartPrompt()
	if a2.ID != a1.ID {
		t.Errorf("expected idempotent, got new id %d vs %d", a2.ID, a1.ID)
	}
	hooks, _ := svc.HooksForEvent(EventProjectStart, "")
	if len(hooks) != 1 {
		t.Errorf("expected exactly 1 project.start hook, got %d", len(hooks))
	}

	// Editing its template changes the resolved prompt.
	svc.UpdateAction(a1.ID, a1.Name, `{"template":"CUSTOM {{repo}}"}`)
	tmpl, _, _ = svc.ResolveProjectStartPrompt("")
	if tmpl != "CUSTOM {{repo}}" {
		t.Errorf("edited template not resolved, got %q", tmpl)
	}
}
