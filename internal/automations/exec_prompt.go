package automations

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// A claude_prompt action is a prompt TEMPLATE. "Executing" it renders the
// template against the run context — its Output is the finished prompt text,
// which the caller delivers however it likes (today: the populate-prompt tmux
// flow that types it into Claude). Keeping render as an action means prompts are
// first-class units of work: they can be scoped global/repo, picked in the UI,
// and later composed into flows, with no special-casing in the engine.

// PromptSpec is the typed config of a KindClaudePrompt action. Template holds
// the text with {{var}} placeholders; Description is a short human label shown
// in pickers.
type PromptSpec struct {
	Template    string `json:"template"`
	Description string `json:"description,omitempty"`
}

// varRe matches {{ name }} placeholders, tolerant of surrounding whitespace.
// Dots are allowed so flow step outputs (steps.<key>.output) substitute. The
// {{secret.*}} form is resolved separately by the http executor BEFORE this
// runs, so it isn't matched here in practice.
var varRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// RenderTemplate substitutes {{var}} placeholders from vars. Unknown
// placeholders render as empty string (so a template that references an
// optional var doesn't leak "{{var}}" into the prompt).
func RenderTemplate(tmpl string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		// Leave {{secret.*}} untouched — the http executor resolves those AFTER
		// var substitution (see resolveSecrets), so they must survive this pass.
		if strings.HasPrefix(name, "secret.") {
			return m
		}
		if vars == nil {
			return ""
		}
		return vars[name]
	})
}

// PromptExecutor renders a claude_prompt action's template against the context.
// It performs no I/O — the rendered text is returned as Output for the caller to
// deliver — so it's safe, deterministic, and trivially testable.
type PromptExecutor struct{}

// Execute renders the template. A malformed spec is an engine error; a template
// that renders to empty is allowed (the caller decides whether to submit).
func (PromptExecutor) Execute(_ context.Context, a Action, rc RunContext) StepResult {
	var spec PromptSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad prompt spec: " + err.Error()}
	}
	rendered := RenderTemplate(spec.Template, rc.Vars)
	return StepResult{Status: StatusOK, Output: rendered}
}

// --- effective prompt resolution -------------------------------------------

// DefaultProjectStartPrompt is Corral's built-in project-start prompt template.
// It is the fallback when neither a repo override nor a global default is set,
// so there is always a sensible prompt. Kept intentionally light-touch.
const DefaultProjectStartPrompt = "You're working in a sandboxed checkout of {{repo}} on branch {{branch}}. " +
	"Explore the codebase, then help with the task at hand."

// ResolveProjectStartPrompt returns the effective project-start prompt template
// for a repo, honoring precedence: the repo's own claude_prompt action bound to
// project.start wins; else a global one; else the built-in default. It returns
// the raw template (unrendered) plus where it came from, so the UI can show
// "repo default" / "global default" / "built-in".
//
// This is the scope-resolution the split-button picker relies on.
func (s *Service) ResolveProjectStartPrompt(repoID string) (template, source string, err error) {
	hooks, err := s.HooksForEvent(EventProjectStart, repoID)
	if err != nil {
		return "", "", err
	}
	// HooksForEvent returns global-first; we want the MOST specific, so scan for
	// a repo-scoped prompt action first, then fall back to a global one.
	var globalTmpl, globalSrc string
	for _, h := range hooks {
		if h.TargetKind != "action" {
			continue
		}
		a, err := s.Action(h.TargetID)
		if err != nil || a.Kind != KindClaudePrompt {
			continue
		}
		var spec PromptSpec
		if json.Unmarshal([]byte(a.Spec), &spec) != nil || strings.TrimSpace(spec.Template) == "" {
			continue
		}
		if h.Scope == ScopeRepo {
			return spec.Template, "repo", nil
		}
		if globalTmpl == "" {
			globalTmpl, globalSrc = spec.Template, "global"
		}
	}
	if globalTmpl != "" {
		return globalTmpl, globalSrc, nil
	}
	return DefaultProjectStartPrompt, "default", nil
}
