package automations

import (
	"testing"

	"github.com/scoutapp/corral/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	s, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s)
}

func TestCreateAndFetchAction(t *testing.T) {
	svc := newService(t)

	got, err := svc.CreateAction(Action{
		Name: "Approve",
		Kind: KindCapability,
		Spec: `{"capability":"pr-approve"}`,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if got.ID == 0 {
		t.Fatal("expected an assigned ID")
	}
	if got.Scope != ScopeGlobal {
		t.Errorf("blank scope should default to global, got %q", got.Scope)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Error("expected timestamps to be set")
	}

	round, err := svc.Action(got.ID)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if round.Name != "Approve" || round.Kind != KindCapability {
		t.Errorf("round-trip mismatch: %+v", round)
	}
}

func TestBlankSpecNormalized(t *testing.T) {
	svc := newService(t)
	got, err := svc.CreateAction(Action{Name: "empty", Kind: KindBash})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if got.Spec != "{}" {
		t.Errorf("blank spec should normalize to {}, got %q", got.Spec)
	}
}

func TestListActionsScoping(t *testing.T) {
	svc := newService(t)

	// One global, one for repo A, one for repo B.
	if _, err := svc.CreateAction(Action{Name: "global-one", Kind: KindSlack}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAction(Action{Name: "repoA", Kind: KindBash, Scope: ScopeRepo, RepoID: "repo-A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAction(Action{Name: "repoB", Kind: KindBash, Scope: ScopeRepo, RepoID: "repo-B"}); err != nil {
		t.Fatal(err)
	}

	// No repo → only global.
	globals, err := svc.ListActions("")
	if err != nil {
		t.Fatalf("ListActions(global): %v", err)
	}
	if len(globals) != 1 || globals[0].Name != "global-one" {
		t.Errorf("global-only list wrong: %+v", globals)
	}

	// Repo A → its own + global, not repo B's.
	a, err := svc.ListActions("repo-A")
	if err != nil {
		t.Fatalf("ListActions(repo-A): %v", err)
	}
	if len(a) != 2 {
		t.Fatalf("repo-A should see 2 (own + global), got %d: %+v", len(a), a)
	}
	for _, act := range a {
		if act.RepoID == "repo-B" {
			t.Error("repo-A must not see repo-B's action")
		}
	}
}

func TestUpdateAndDeleteAction(t *testing.T) {
	svc := newService(t)
	a, err := svc.CreateAction(Action{Name: "old", Kind: KindWebhook, Spec: `{"url":"x"}`})
	if err != nil {
		t.Fatal(err)
	}

	upd, err := svc.UpdateAction(a.ID, "new", `{"url":"y"}`)
	if err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}
	if upd.Name != "new" || upd.Spec != `{"url":"y"}` {
		t.Errorf("update didn't apply: %+v", upd)
	}

	if err := svc.DeleteAction(a.ID); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	if _, err := svc.Action(a.ID); err == nil {
		t.Error("expected error fetching deleted action")
	}
}

func TestHooksForEventOrderingAndScope(t *testing.T) {
	svc := newService(t)

	// A couple of target actions to point hooks at.
	act, _ := svc.CreateAction(Action{Name: "notify", Kind: KindSlack})

	// Global hook, a repo-A hook, a disabled hook, and a different-event hook.
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "action", TargetID: act.ID, Position: 1, Enabled: true})
	mustHook(t, svc, Hook{Event: EventPRApprove, Scope: ScopeRepo, RepoID: "repo-A", TargetKind: "action", TargetID: act.ID, Position: 0, Enabled: true})
	mustHook(t, svc, Hook{Event: EventPRApprove, TargetKind: "action", TargetID: act.ID, Position: 5, Enabled: false})
	mustHook(t, svc, Hook{Event: EventPRMerge, TargetKind: "action", TargetID: act.ID, Enabled: true})

	// Repo-A, pr.approve: sees the global + its own, ordered global-first,
	// disabled excluded, other events excluded.
	hooks, err := svc.HooksForEvent(EventPRApprove, "repo-A")
	if err != nil {
		t.Fatalf("HooksForEvent: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 enabled approve hooks for repo-A, got %d: %+v", len(hooks), hooks)
	}
	if hooks[0].Scope != ScopeGlobal {
		t.Errorf("global hook should sort first, got %q", hooks[0].Scope)
	}
	if hooks[1].RepoID != "repo-A" {
		t.Errorf("repo hook should sort second, got %+v", hooks[1])
	}

	// A repo with no own hooks sees only the global one.
	other, err := svc.HooksForEvent(EventPRApprove, "repo-Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Scope != ScopeGlobal {
		t.Errorf("repo-Z should see only the global hook, got %+v", other)
	}
}

func mustHook(t *testing.T, svc *Service, h Hook) {
	t.Helper()
	if _, err := svc.CreateHook(h); err != nil {
		t.Fatalf("CreateHook(%+v): %v", h, err)
	}
}
