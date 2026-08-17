package automations

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The named-prompt LIBRARY: reusable prompts a user saves by name and picks when
// starting a project or working an issue, alongside the fixed catalog. It reuses
// the claude_prompt action storage — a library prompt is just a claude_prompt
// action whose Name is NOT the reserved "prompt:<key>" form the catalog overrides
// use. No new schema; ListActions already returns these, and the project-start
// preset picker already surfaces them.
//
//	catalog override → claude_prompt action named "prompt:project.start"
//	library prompt   → claude_prompt action named "Thorough refactor"

// reservedPromptPrefix is the name prefix catalog overrides use; library names
// may not start with it, so the two never collide.
const reservedPromptPrefix = "prompt:"

// NamedPrompt is a library entry.
type NamedPrompt struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Template    string `json:"template"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`  // global | repo
	RepoID      string `json:"repoId,omitempty"`
}

// isLibraryName reports whether an action name belongs to the user library (i.e.
// isn't a reserved catalog-override name).
func isLibraryName(name string) bool {
	return !strings.HasPrefix(name, reservedPromptPrefix)
}

// ListNamedPrompts returns the saved library prompts visible at repoID (the
// repo's own + global ones), newest catalog-override names excluded. repoID ""
// lists global only.
func (s *Service) ListNamedPrompts(repoID string) ([]NamedPrompt, error) {
	actions, err := s.ListActions(repoID)
	if err != nil {
		return nil, err
	}
	out := []NamedPrompt{}
	for _, a := range actions {
		if a.Kind != KindClaudePrompt || !isLibraryName(a.Name) {
			continue
		}
		out = append(out, toNamedPrompt(a))
	}
	return out, nil
}

// NamedPrompt returns one library prompt by id (must be a claude_prompt with a
// library name).
func (s *Service) NamedPrompt(id int64) (NamedPrompt, error) {
	a, err := s.Action(id)
	if err != nil {
		return NamedPrompt{}, err
	}
	if a.Kind != KindClaudePrompt || !isLibraryName(a.Name) {
		return NamedPrompt{}, fmt.Errorf("action %d is not a library prompt", id)
	}
	return toNamedPrompt(a), nil
}

// CreateNamedPrompt saves a new library prompt. The name must be non-empty and
// must not use the reserved catalog-override prefix.
func (s *Service) CreateNamedPrompt(name, template, description, repoID string) (NamedPrompt, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return NamedPrompt{}, fmt.Errorf("named prompt: name is required")
	}
	if !isLibraryName(name) {
		return NamedPrompt{}, fmt.Errorf("named prompt: name may not start with %q (reserved)", reservedPromptPrefix)
	}
	scope := ScopeGlobal
	if repoID != "" {
		scope = ScopeRepo
	}
	spec, _ := json.Marshal(PromptSpec{Template: template, Description: description})
	a, err := s.CreateAction(Action{Name: name, Kind: KindClaudePrompt, Spec: string(spec), Scope: scope, RepoID: repoID})
	if err != nil {
		return NamedPrompt{}, err
	}
	return toNamedPrompt(a), nil
}

// UpdateNamedPrompt edits a library prompt's name/template/description. Guards
// that the target is a library prompt and the new name stays out of the reserved
// namespace.
func (s *Service) UpdateNamedPrompt(id int64, name, template, description string) (NamedPrompt, error) {
	existing, err := s.NamedPrompt(id) // also enforces "is a library prompt"
	if err != nil {
		return NamedPrompt{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = existing.Name
	}
	if !isLibraryName(name) {
		return NamedPrompt{}, fmt.Errorf("named prompt: name may not start with %q (reserved)", reservedPromptPrefix)
	}
	spec, _ := json.Marshal(PromptSpec{Template: template, Description: description})
	a, err := s.UpdateAction(id, name, string(spec))
	if err != nil {
		return NamedPrompt{}, err
	}
	return toNamedPrompt(a), nil
}

// DeleteNamedPrompt removes a library prompt (guarded to library prompts only, so
// a catalog override can't be deleted through this path).
func (s *Service) DeleteNamedPrompt(id int64) error {
	if _, err := s.NamedPrompt(id); err != nil {
		return err
	}
	return s.DeleteAction(id)
}

func toNamedPrompt(a Action) NamedPrompt {
	var spec PromptSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	return NamedPrompt{
		ID:          a.ID,
		Name:        a.Name,
		Template:    spec.Template,
		Description: spec.Description,
		Scope:       a.Scope,
		RepoID:      a.RepoID,
	}
}
