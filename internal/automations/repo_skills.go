package automations

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Per-repo skills + agent context (milestone #5): a repo can carry its own skills
// and a CLAUDE.md-style context that get injected into a sandbox checkout of that
// repo, so Claude lands with the right capabilities + knowledge for that codebase.
//
// Both reuse the auto_actions store (repo-scoped), like prompts/flows:
//   - a skill        → kind='skill', name=<skill name>, spec={content: SKILL.md}
//   - the context    → kind='agent_context', ONE per repo, spec={content: CLAUDE.md}
//
// The delivery (writing them into the workspace at project-create) lives in the
// dashboard; this is just storage.

// skillSpec / contextSpec hold the markdown content in the action's spec_json.
type skillSpec struct {
	Content string `json:"content"`
}

// RepoSkill is a per-repo skill entry.
type RepoSkill struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	RepoID  string `json:"repoId"`
}

// ListRepoSkills returns a repo's skills (repo-scoped only — global skills come
// from the sandbox bundle / host, not this store).
func (s *Service) ListRepoSkills(repoID string) ([]RepoSkill, error) {
	if repoID == "" {
		return nil, fmt.Errorf("repo skills: repoID is required")
	}
	actions, err := s.ListActions(repoID)
	if err != nil {
		return nil, err
	}
	out := []RepoSkill{}
	for _, a := range actions {
		if a.Kind != KindSkill || a.Scope != ScopeRepo || a.RepoID != repoID {
			continue
		}
		out = append(out, toRepoSkill(a))
	}
	return out, nil
}

// CreateRepoSkill saves a new skill for a repo. Name must be a safe, non-empty
// slug-ish string (it becomes a directory name in the sandbox).
func (s *Service) CreateRepoSkill(repoID, name, content string) (RepoSkill, error) {
	if repoID == "" {
		return RepoSkill{}, fmt.Errorf("repo skills: repoID is required")
	}
	name = strings.TrimSpace(name)
	if !validSkillName(name) {
		return RepoSkill{}, fmt.Errorf("repo skills: name must be non-empty letters, digits, - or _")
	}
	spec, _ := json.Marshal(skillSpec{Content: content})
	a, err := s.CreateAction(Action{Name: name, Kind: KindSkill, Spec: string(spec), Scope: ScopeRepo, RepoID: repoID})
	if err != nil {
		return RepoSkill{}, err
	}
	return toRepoSkill(a), nil
}

// UpdateRepoSkill edits a skill's name/content (guarded to skill actions).
func (s *Service) UpdateRepoSkill(id int64, name, content string) (RepoSkill, error) {
	a, err := s.Action(id)
	if err != nil {
		return RepoSkill{}, err
	}
	if a.Kind != KindSkill {
		return RepoSkill{}, fmt.Errorf("action %d is not a skill", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = a.Name
	}
	if !validSkillName(name) {
		return RepoSkill{}, fmt.Errorf("repo skills: name must be non-empty letters, digits, - or _")
	}
	spec, _ := json.Marshal(skillSpec{Content: content})
	updated, err := s.UpdateAction(id, name, string(spec))
	if err != nil {
		return RepoSkill{}, err
	}
	return toRepoSkill(updated), nil
}

// DeleteRepoSkill removes a skill (guarded to skill actions).
func (s *Service) DeleteRepoSkill(id int64) error {
	a, err := s.Action(id)
	if err != nil {
		return err
	}
	if a.Kind != KindSkill {
		return fmt.Errorf("action %d is not a skill", id)
	}
	return s.DeleteAction(id)
}

// --- agent context (CLAUDE.md), one per repo -------------------------------

// RepoAgentContext returns a repo's saved CLAUDE.md context ("" if none).
func (s *Service) RepoAgentContext(repoID string) (string, error) {
	if repoID == "" {
		return "", nil
	}
	a, ok := s.agentContextAction(repoID)
	if !ok {
		return "", nil
	}
	var spec skillSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	return spec.Content, nil
}

// SetRepoAgentContext upserts a repo's CLAUDE.md context. Empty content clears it.
func (s *Service) SetRepoAgentContext(repoID, content string) error {
	if repoID == "" {
		return fmt.Errorf("agent context: repoID is required")
	}
	existing, ok := s.agentContextAction(repoID)
	if strings.TrimSpace(content) == "" {
		if ok {
			return s.DeleteAction(existing.ID)
		}
		return nil
	}
	spec, _ := json.Marshal(skillSpec{Content: content})
	if ok {
		_, err := s.UpdateAction(existing.ID, existing.Name, string(spec))
		return err
	}
	_, err := s.CreateAction(Action{Name: "agent-context", Kind: KindAgentContext, Spec: string(spec), Scope: ScopeRepo, RepoID: repoID})
	return err
}

// agentContextAction finds a repo's single agent_context action.
func (s *Service) agentContextAction(repoID string) (Action, bool) {
	actions, err := s.ListActions(repoID)
	if err != nil {
		return Action{}, false
	}
	for _, a := range actions {
		if a.Kind == KindAgentContext && a.Scope == ScopeRepo && a.RepoID == repoID {
			return a, true
		}
	}
	return Action{}, false
}

func toRepoSkill(a Action) RepoSkill {
	var spec skillSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	return RepoSkill{ID: a.ID, Name: a.Name, Content: spec.Content, RepoID: a.RepoID}
}

// validSkillName gates a skill name to a filesystem-safe slug (it becomes a dir
// name in the sandbox: .corral/skills/<name>/SKILL.md).
func validSkillName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
