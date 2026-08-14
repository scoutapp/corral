package automations

import (
	"fmt"
	"regexp"
	"strings"
)

// Tool adapter: describe actions as callable tools for an LLM (the host Claude),
// and invoke them by tool name. This is the thin bridge that lets the engine's
// units of work become Claude tools — the same `action:run` path, wrapped in a
// name + input schema. The actual MCP/tool-server plumbing lives above this
// (dashboard); here we only produce descriptors and resolve a tool name back to
// an action.
//
// Exposure is deliberately conservative: only actions whose kind is safe to
// describe generically are listed, and the DASHBOARD gates the whole surface
// behind a user-permission flag. The engine never decides on its own that Claude
// may act.

// ToolDescriptor is a provider-neutral description of one action-as-tool: a
// stable name, a human/LLM-readable description, and a JSON-schema-ish input
// spec (the context vars the action understands).
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ActionID    int64          `json:"actionId"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolNameRe keeps tool names to a safe identifier charset.
var toolNameRe = regexp.MustCompile(`[^a-z0-9_]+`)

// ToolName derives a stable tool name from an action: "corral_<kind>_<slug>".
// Slugs are lowercased and non-identifier chars collapse to underscore.
func ToolName(a Action) string {
	slug := toolNameRe.ReplaceAllString(strings.ToLower(a.Name), "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = fmt.Sprintf("action_%d", a.ID)
	}
	return "corral_" + slug
}

// DescribeAction turns an action into a tool descriptor. The input schema
// advertises the run-context vars the action's kind consumes, so the caller
// (Claude) knows what to pass.
func DescribeAction(a Action) ToolDescriptor {
	return ToolDescriptor{
		Name:        ToolName(a),
		Description: toolDescription(a),
		ActionID:    a.ID,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": inputProps(a.Kind),
		},
	}
}

// ToolManifest returns descriptors for the actions that should be exposed as
// tools. Only global-scope actions are exposed (repo tools would need repo
// disambiguation the LLM can't safely infer); the dashboard further gates this
// behind the permission flag.
func (s *Service) ToolManifest() ([]ToolDescriptor, error) {
	actions, err := s.ListActions("")
	if err != nil {
		return nil, err
	}
	out := []ToolDescriptor{}
	for _, a := range actions {
		out = append(out, DescribeAction(a))
	}
	return out, nil
}

// ActionForTool resolves a tool name back to its action.
func (s *Service) ActionForTool(name string) (Action, error) {
	actions, err := s.ListActions("")
	if err != nil {
		return Action{}, err
	}
	for _, a := range actions {
		if ToolName(a) == name {
			return a, nil
		}
	}
	return Action{}, fmt.Errorf("no tool named %q", name)
}

func toolDescription(a Action) string {
	switch a.Kind {
	case KindCapability:
		return fmt.Sprintf("Corral automation %q: a PR capability. Requires owner_name and pr_number.", a.Name)
	case KindClaudePrompt:
		return fmt.Sprintf("Corral automation %q: renders a prompt template from the given vars.", a.Name)
	case KindWebhook, KindSlack:
		return fmt.Sprintf("Corral automation %q: sends an outbound notification.", a.Name)
	case KindBash:
		return fmt.Sprintf("Corral automation %q: runs a script (event vars exposed as CORRAL_* env).", a.Name)
	default:
		return fmt.Sprintf("Corral automation %q.", a.Name)
	}
}

// inputProps advertises the context vars a kind consumes, as a minimal JSON
// schema properties map.
func inputProps(kind string) map[string]any {
	str := map[string]any{"type": "string"}
	switch kind {
	case KindCapability:
		return map[string]any{
			"owner_name": str, "pr_number": str, "body": str, "method": str, "head_sha": str,
		}
	case KindClaudePrompt:
		return map[string]any{"repo": str, "branch": str, "pr_number": str, "pr_title": str, "pr_url": str}
	default:
		return map[string]any{"repo": str, "pr_number": str, "pr_url": str}
	}
}
