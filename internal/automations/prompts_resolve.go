package automations

import (
	"encoding/json"
	"strings"
)

// Three-level prompt resolution: built-in default → global override → repo
// override, with the REPO override taking highest priority. Overrides are stored
// as claude_prompt actions named "prompt:<key>" (scope global or repo), reusing
// the actions table — no new schema. Call sites render the effective template
// via RenderPrompt; the carousel edits the overrides via the /api/prompts
// endpoints.

// promptActionName is the reserved action name that holds a prompt override.
func promptActionName(key string) string { return "prompt:" + key }

// ResolvePrompt returns the effective template for a prompt key and its source
// ("repo" | "global" | "default"). Repo override wins, then global, then the
// built-in catalog default. An unknown key yields ("", "unknown").
func (s *Service) ResolvePrompt(key, repoID string) (template, source string) {
	def, ok := PromptDefFor(key)
	if !ok {
		return "", "unknown"
	}

	want := promptActionName(key)
	// ListActions(repoID) returns the repo's own + global; scan for our override
	// at each scope. Prefer repo.
	actions, err := s.ListActions(repoID)
	if err == nil {
		var globalTmpl string
		for _, a := range actions {
			if a.Kind != KindClaudePrompt || a.Name != want {
				continue
			}
			var spec PromptSpec
			if json.Unmarshal([]byte(a.Spec), &spec) != nil || strings.TrimSpace(spec.Template) == "" {
				continue
			}
			if a.Scope == ScopeRepo && a.RepoID == repoID && repoID != "" {
				return spec.Template, "repo"
			}
			if a.Scope == ScopeGlobal && globalTmpl == "" {
				globalTmpl = spec.Template
			}
		}
		if globalTmpl != "" {
			return globalTmpl, "global"
		}
	}
	return def.Default, "default"
}

// RenderPrompt resolves the effective template for a key and fills its slots.
// This is what every call site uses instead of a hard-coded string: pass the
// slot values, get the finished prompt. Unknown key → "".
func (s *Service) RenderPrompt(key, repoID string, vars map[string]string) string {
	tmpl, source := s.ResolvePrompt(key, repoID)
	if source == "unknown" {
		return ""
	}
	return RenderTemplate(tmpl, vars)
}

// RenderSSHGuidance renders the (editable) SSH push-guidance sentence for a
// repo, filling {{ssh_remote}} with git@github.com:<repoOwnerName>.git. Returns
// "" when repoOwnerName is empty (no GitHub remote → no guidance). The project
// prompts fill their {{ssh_guidance}} slot with this only when a key is loaded.
func (s *Service) RenderSSHGuidance(repoID, repoOwnerName string) string {
	if strings.TrimSpace(repoOwnerName) == "" {
		return ""
	}
	remote := "git@github.com:" + repoOwnerName + ".git"
	return s.RenderPrompt(PromptSSHGuidance, repoID, map[string]string{"ssh_remote": remote})
}

// RenderEngineeringPrinciples renders the (editable) shared engineering-principles
// snippet for a repo. The project prompts fill their {{engineering_principles}}
// slot with this. Repo override wins, then global, then the built-in default.
func (s *Service) RenderEngineeringPrinciples(repoID string) string {
	return s.RenderPrompt(PromptEngPrinciples, repoID, nil)
}

// SetPromptOverride creates or updates the override action for a key at the
// given scope (repoID empty = global). Returns the stored action.
func (s *Service) SetPromptOverride(key, repoID, template string) (Action, error) {
	name := promptActionName(key)
	spec, _ := json.Marshal(PromptSpec{Template: template})

	// Find an existing override at this exact scope to update in place.
	actions, err := s.ListActions(repoID)
	if err == nil {
		for _, a := range actions {
			if a.Kind != KindClaudePrompt || a.Name != name {
				continue
			}
			atRepo := a.Scope == ScopeRepo && a.RepoID == repoID && repoID != ""
			atGlobal := a.Scope == ScopeGlobal && repoID == ""
			if atRepo || atGlobal {
				return s.UpdateAction(a.ID, name, string(spec))
			}
		}
	}

	scope := ScopeGlobal
	if repoID != "" {
		scope = ScopeRepo
	}
	return s.CreateAction(Action{Name: name, Kind: KindClaudePrompt, Spec: string(spec), Scope: scope, RepoID: repoID})
}

// ClearPromptOverride removes the override at the given scope (repoID empty =
// global), resetting that level back to the next one down. Returns nil if there
// was nothing to clear.
func (s *Service) ClearPromptOverride(key, repoID string) error {
	name := promptActionName(key)
	actions, err := s.ListActions(repoID)
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.Kind != KindClaudePrompt || a.Name != name {
			continue
		}
		atRepo := a.Scope == ScopeRepo && a.RepoID == repoID && repoID != ""
		atGlobal := a.Scope == ScopeGlobal && repoID == ""
		if atRepo || atGlobal {
			return s.DeleteAction(a.ID)
		}
	}
	return nil
}
