package automations

import (
	"strings"
	"testing"
)

func TestNamedPromptCRUD(t *testing.T) {
	svc := newService(t)

	np, err := svc.CreateNamedPrompt("Thorough refactor", "Refactor {{repo}} carefully.", "for big cleanups", "")
	if err != nil {
		t.Fatal(err)
	}
	if np.Name != "Thorough refactor" || np.Template == "" || np.Scope != ScopeGlobal {
		t.Fatalf("created prompt wrong: %+v", np)
	}

	// List returns it.
	list, _ := svc.ListNamedPrompts("")
	if len(list) != 1 || list[0].ID != np.ID {
		t.Fatalf("list wrong: %+v", list)
	}

	// Update name + template.
	up, err := svc.UpdateNamedPrompt(np.ID, "Careful refactor", "Refactor {{repo}} very carefully.", "updated")
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "Careful refactor" || !strings.Contains(up.Template, "very carefully") {
		t.Errorf("update wrong: %+v", up)
	}

	// Delete.
	if err := svc.DeleteNamedPrompt(np.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.ListNamedPrompts(""); len(list) != 0 {
		t.Errorf("expected empty after delete, got %d", len(list))
	}
}

func TestNamedPromptRejectsReservedName(t *testing.T) {
	svc := newService(t)
	if _, err := svc.CreateNamedPrompt("prompt:project.start", "x", "", ""); err == nil {
		t.Error("expected reserved-prefix name to be rejected")
	}
	if _, err := svc.CreateNamedPrompt("", "x", "", ""); err == nil {
		t.Error("expected empty name to be rejected")
	}
}

// The library must not surface or mutate catalog overrides (name "prompt:<key>").
func TestNamedPromptSeparateFromCatalogOverrides(t *testing.T) {
	svc := newService(t)

	// A catalog override (via the override API) + a library prompt.
	if _, err := svc.SetPromptOverride("project.start", "", "custom start"); err != nil {
		t.Fatal(err)
	}
	lib, _ := svc.CreateNamedPrompt("My preset", "hello", "", "")

	// List shows only the library prompt, not the override.
	list, _ := svc.ListNamedPrompts("")
	if len(list) != 1 || list[0].Name != "My preset" {
		t.Fatalf("library leaked a catalog override or missed the lib prompt: %+v", list)
	}

	// The override action's id can't be deleted/updated through the library path.
	// Find the override action id.
	actions, _ := svc.ListActions("")
	var overrideID int64
	for _, a := range actions {
		if a.Name == "prompt:project.start" {
			overrideID = a.ID
		}
	}
	if overrideID == 0 {
		t.Fatal("override action not found")
	}
	if err := svc.DeleteNamedPrompt(overrideID); err == nil {
		t.Error("deleting a catalog override via the library path should fail")
	}
	if _, err := svc.UpdateNamedPrompt(overrideID, "x", "y", ""); err == nil {
		t.Error("updating a catalog override via the library path should fail")
	}
	// The library prompt itself still works.
	if _, err := svc.NamedPrompt(lib.ID); err != nil {
		t.Errorf("library prompt lookup failed: %v", err)
	}
}

func TestNamedPromptRepoScope(t *testing.T) {
	svc := newService(t)
	svc.CreateNamedPrompt("global one", "g", "", "")
	svc.CreateNamedPrompt("repo one", "r", "", "repo-123")

	// Global scope lists only global.
	if g, _ := svc.ListNamedPrompts(""); len(g) != 1 || g[0].Name != "global one" {
		t.Errorf("global list wrong: %+v", g)
	}
	// Repo scope lists repo's own + global.
	r, _ := svc.ListNamedPrompts("repo-123")
	if len(r) != 2 {
		t.Errorf("repo list should include repo + global, got %d: %+v", len(r), r)
	}
}
