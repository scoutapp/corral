package automations

import "testing"

func TestToolName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Approve PR", "corral_approve_pr"},
		{"slack-notify!", "corral_slack_notify"},
		{"  Weird   Name  ", "corral_weird_name"},
	}
	for _, c := range cases {
		if got := ToolName(Action{Name: c.name, ID: 1}); got != c.want {
			t.Errorf("ToolName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// Empty name falls back to the id.
	if got := ToolName(Action{Name: "", ID: 7}); got != "corral_action_7" {
		t.Errorf("empty name fallback wrong: %q", got)
	}
}

func TestDescribeAndResolveTool(t *testing.T) {
	svc := newService(t)
	a, _ := svc.CreateAction(Action{Name: "Approve", Kind: KindCapability, Spec: `{"capability":"pr-approve"}`})

	desc := DescribeAction(a)
	if desc.Name != "corral_approve" || desc.ActionID != a.ID {
		t.Fatalf("descriptor wrong: %+v", desc)
	}
	// Capability input schema advertises owner_name/pr_number.
	props, _ := desc.InputSchema["properties"].(map[string]any)
	if _, ok := props["owner_name"]; !ok {
		t.Error("capability tool should advertise owner_name input")
	}

	// Manifest lists it; ActionForTool resolves it back.
	tools, err := svc.ToolManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	got, err := svc.ActionForTool("corral_approve")
	if err != nil {
		t.Fatalf("ActionForTool: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("resolved wrong action: %+v", got)
	}

	if _, err := svc.ActionForTool("corral_nope"); err == nil {
		t.Error("expected error resolving unknown tool")
	}
}
