package automations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func mcpAction(spec string) Action { return Action{Kind: KindMCP, Spec: spec} }

func TestMCPExecutorRunsClaudeWithServerTools(t *testing.T) {
	var gotArgs []string
	e := MCPExecutor{
		ClaudeBin: "claude",
		run: func(ctx context.Context, bin string, args ...string) (string, error) {
			gotArgs = args
			return "  [{\"id\":1}]  ", nil
		},
	}
	res := e.Execute(context.Background(), mcpAction(`{"server":"Sentry","prompt":"list errors for {{repo}}"}`),
		RunContext{Vars: map[string]string{"repo": "acme/widget"}})

	if res.Status != StatusOK {
		t.Fatalf("status = %q, err = %q", res.Status, res.Err)
	}
	// Output is trimmed.
	if res.Output != `[{"id":1}]` {
		t.Errorf("output = %q", res.Output)
	}
	joined := strings.Join(gotArgs, " ")
	// Prompt was rendered ({{repo}} substituted).
	if !strings.Contains(joined, "list errors for acme/widget") {
		t.Errorf("prompt not rendered into args: %s", joined)
	}
	// Only this server's MCP tools are allowed (sanitized: Sentry → sentry).
	if !strings.Contains(joined, "--allowedTools mcp__sentry__*") {
		t.Errorf("expected --allowedTools mcp__sentry__* ; got %s", joined)
	}
	if !strings.Contains(joined, "-p ") {
		t.Errorf("expected -p prompt flag; got %s", joined)
	}
}

func TestMCPExecutorValidation(t *testing.T) {
	e := MCPExecutor{ClaudeBin: "claude", run: func(context.Context, string, ...string) (string, error) { return "", nil }}
	cases := map[string]string{
		"missing server": `{"prompt":"x"}`,
		"missing prompt": `{"server":"s"}`,
		"bad json":       `{`,
	}
	for name, spec := range cases {
		if res := e.Execute(context.Background(), mcpAction(spec), RunContext{}); res.Status != StatusError {
			t.Errorf("%s: expected error status, got %q", name, res.Status)
		}
	}
}

func TestMCPExecutorNoClaudeBin(t *testing.T) {
	e := MCPExecutor{ClaudeBin: ""} // unavailable
	res := e.Execute(context.Background(), mcpAction(`{"server":"s","prompt":"p"}`), RunContext{})
	if res.Status != StatusError || !strings.Contains(res.Err, "claude") {
		t.Errorf("expected a clear claude-unavailable error, got %+v", res)
	}
}

func TestMCPExecutorSurfacesRunError(t *testing.T) {
	e := MCPExecutor{
		ClaudeBin: "claude",
		run:       func(context.Context, string, ...string) (string, error) { return "boom output", errors.New("exit 1") },
	}
	res := e.Execute(context.Background(), mcpAction(`{"server":"s","prompt":"p"}`), RunContext{})
	if res.Status != StatusError {
		t.Fatalf("expected error status")
	}
	// The output is preferred as the error message when present.
	if res.Err != "boom output" || res.Output != "boom output" {
		t.Errorf("error surfacing wrong: %+v", res)
	}
}

func TestSanitizeServerName(t *testing.T) {
	cases := map[string]string{
		"Sentry":              "sentry",
		"claude.ai Scout MCP": "claude_ai_scout_mcp",
		"linear":              "linear",
	}
	for in, want := range cases {
		if got := sanitizeServerName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Registered in the built-in registry.
func TestMCPKindRegistered(t *testing.T) {
	r := RegistryWith(RegistryOptions{ClaudeBin: "claude"})
	if r.executorFor(KindMCP) == nil {
		t.Error("KindMCP not registered")
	}
}
