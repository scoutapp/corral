package automations

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Skills + agent context: a repo can carry skills and a CLAUDE.md-style context
// that get injected into a sandbox checkout of that repo, so Claude lands with the
// right capabilities + knowledge for that codebase.
//
// Skills come at two scopes:
//   - GLOBAL skills (kind='skill', scope='global') — a shared catalog. Each has an
//     AutoAll flag; when set, the skill is injected into EVERY repo's sandbox by
//     default. A repo can override that default per-skill (see skill_pref below).
//   - REPO skills (kind='skill', scope='repo') — a repo's own skills. A repo skill
//     overrides a global one of the same name (its content wins).
//
// A GLOBAL skill's per-repo enable/disable lives in a small override row:
//   - kind='skill_pref', scope='repo', repo_id=<id>, name=<global skill name>,
//     spec={enabled: bool}. ABSENCE of a row means "inherit the global's AutoAll".
//
// The agent context is one per repo (kind='agent_context', scope='repo').
//
// The delivery (writing them into the workspace at project-create) lives in the
// dashboard; this is just storage. EffectiveSkills is the resolved set injected.

// skillSpec holds a skill's markdown content in the action's spec_json. AutoAll is
// meaningful only on GLOBAL skills: when true the skill is injected into every
// repo unless that repo disables it.
type skillSpec struct {
	Content string `json:"content"`
	AutoAll bool   `json:"autoAll,omitempty"`
}

// skillPrefSpec is a repo's explicit enable/disable of a global skill.
type skillPrefSpec struct {
	Enabled bool `json:"enabled"`
}

// RepoSkill is a skill entry. Scope is "global" or "repo"; AutoAll is set only on
// global skills. For a repo skill, RepoID is the owning repo; for a global skill
// it is empty.
type RepoSkill struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	RepoID  string `json:"repoId"`
	Scope   string `json:"scope"`
	AutoAll bool   `json:"autoAll"`
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

// --- global skills ---------------------------------------------------------

// ListGlobalSkills returns the shared catalog of global skills.
func (s *Service) ListGlobalSkills() ([]RepoSkill, error) {
	actions, err := s.ListActions("") // global-only
	if err != nil {
		return nil, err
	}
	out := []RepoSkill{}
	for _, a := range actions {
		if a.Kind != KindSkill || a.Scope != ScopeGlobal {
			continue
		}
		out = append(out, toRepoSkill(a))
	}
	return out, nil
}

// CreateGlobalSkill saves a new global skill. autoAll=true injects it into every
// repo's sandbox by default (a repo can still disable it).
func (s *Service) CreateGlobalSkill(name, content string, autoAll bool) (RepoSkill, error) {
	name = strings.TrimSpace(name)
	if !validSkillName(name) {
		return RepoSkill{}, fmt.Errorf("global skills: name must be non-empty letters, digits, - or _")
	}
	spec, _ := json.Marshal(skillSpec{Content: content, AutoAll: autoAll})
	a, err := s.CreateAction(Action{Name: name, Kind: KindSkill, Spec: string(spec), Scope: ScopeGlobal})
	if err != nil {
		return RepoSkill{}, err
	}
	return toRepoSkill(a), nil
}

// UpdateGlobalSkill edits a global skill's name/content/autoAll (guarded).
func (s *Service) UpdateGlobalSkill(id int64, name, content string, autoAll bool) (RepoSkill, error) {
	a, err := s.Action(id)
	if err != nil {
		return RepoSkill{}, err
	}
	if a.Kind != KindSkill || a.Scope != ScopeGlobal {
		return RepoSkill{}, fmt.Errorf("action %d is not a global skill", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = a.Name
	}
	if !validSkillName(name) {
		return RepoSkill{}, fmt.Errorf("global skills: name must be non-empty letters, digits, - or _")
	}
	spec, _ := json.Marshal(skillSpec{Content: content, AutoAll: autoAll})
	updated, err := s.UpdateAction(id, name, string(spec))
	if err != nil {
		return RepoSkill{}, err
	}
	return toRepoSkill(updated), nil
}

// DeleteGlobalSkill removes a global skill (guarded).
func (s *Service) DeleteGlobalSkill(id int64) error {
	a, err := s.Action(id)
	if err != nil {
		return err
	}
	if a.Kind != KindSkill || a.Scope != ScopeGlobal {
		return fmt.Errorf("action %d is not a global skill", id)
	}
	return s.DeleteAction(id)
}

// PromoteSkillToGlobal turns a repo skill into a global one (same id, scope flips
// to global, repo_id cleared), so it can be reused across all repos. autoAll sets
// the injected-everywhere default.
func (s *Service) PromoteSkillToGlobal(id int64, autoAll bool) (RepoSkill, error) {
	a, err := s.Action(id)
	if err != nil {
		return RepoSkill{}, err
	}
	if a.Kind != KindSkill || a.Scope != ScopeRepo {
		return RepoSkill{}, fmt.Errorf("action %d is not a repo skill", id)
	}
	var spec skillSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	spec.AutoAll = autoAll
	newSpec, _ := json.Marshal(spec)
	promoted, err := s.promoteActionToGlobal(id, string(newSpec))
	if err != nil {
		return RepoSkill{}, err
	}
	return toRepoSkill(promoted), nil
}

// --- per-repo enable/disable of a global skill ------------------------------

// skillPref returns a repo's explicit enable/disable of the named global skill,
// and whether such an override exists.
func (s *Service) skillPref(repoID, name string) (Action, bool) {
	actions, err := s.ListActions(repoID)
	if err != nil {
		return Action{}, false
	}
	for _, a := range actions {
		if a.Kind == KindSkillPref && a.Scope == ScopeRepo && a.RepoID == repoID && a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

// SetRepoSkillEnabled records a repo's explicit choice for a global skill:
// enabled=true forces it on for this repo, enabled=false forces it off. This
// overrides the global skill's AutoAll default. Upserts by (repo, name).
func (s *Service) SetRepoSkillEnabled(repoID, name string, enabled bool) error {
	if repoID == "" {
		return fmt.Errorf("skill pref: repoID is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skill pref: name is required")
	}
	spec, _ := json.Marshal(skillPrefSpec{Enabled: enabled})
	if existing, ok := s.skillPref(repoID, name); ok {
		_, err := s.UpdateAction(existing.ID, name, string(spec))
		return err
	}
	_, err := s.CreateAction(Action{Name: name, Kind: KindSkillPref, Spec: string(spec), Scope: ScopeRepo, RepoID: repoID})
	return err
}

// ClearRepoSkillPref removes a repo's explicit override for a global skill,
// reverting it to inherit the global's AutoAll default. No-op if none exists.
func (s *Service) ClearRepoSkillPref(repoID, name string) error {
	if existing, ok := s.skillPref(repoID, strings.TrimSpace(name)); ok {
		return s.DeleteAction(existing.ID)
	}
	return nil
}

// repoSkillPrefs returns a repo's explicit global-skill overrides as name→enabled.
func (s *Service) repoSkillPrefs(repoID string) map[string]bool {
	prefs := map[string]bool{}
	actions, err := s.ListActions(repoID)
	if err != nil {
		return prefs
	}
	for _, a := range actions {
		if a.Kind != KindSkillPref || a.Scope != ScopeRepo || a.RepoID != repoID {
			continue
		}
		var spec skillPrefSpec
		_ = json.Unmarshal([]byte(a.Spec), &spec)
		prefs[a.Name] = spec.Enabled
	}
	return prefs
}

// EffectiveSkills resolves the skills injected for a repo: the global skills that
// apply (AutoAll, unless the repo overrides), plus the repo's own skills. A repo
// skill overrides a global one of the same name (its content wins). This is what
// the injector writes into the sandbox.
func (s *Service) EffectiveSkills(repoID string) ([]RepoSkill, error) {
	globals, err := s.ListGlobalSkills()
	if err != nil {
		return nil, err
	}
	byName := map[string]RepoSkill{}
	if repoID != "" {
		prefs := s.repoSkillPrefs(repoID)
		for _, g := range globals {
			on := g.AutoAll
			if v, ok := prefs[g.Name]; ok {
				on = v // explicit repo choice overrides AutoAll
			}
			if on {
				byName[g.Name] = g
			}
		}
		own, err := s.ListRepoSkills(repoID)
		if err != nil {
			return nil, err
		}
		for _, r := range own {
			byName[r.Name] = r // repo skill wins over a global of the same name
		}
	} else {
		for _, g := range globals {
			if g.AutoAll {
				byName[g.Name] = g
			}
		}
	}

	out := make([]RepoSkill, 0, len(byName))
	for _, sk := range byName {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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

// RepoAgentContextMeta returns a repo's context along with the RFC3339-ish
// updatedAt stamp of when it was last saved/regenerated (SQLite's
// "YYYY-MM-DD HH:MM:SS" UTC). ok is false when the repo has no saved context.
// Callers use updatedAt to compute staleness.
func (s *Service) RepoAgentContextMeta(repoID string) (content, updatedAt string, ok bool) {
	if repoID == "" {
		return "", "", false
	}
	a, found := s.agentContextAction(repoID)
	if !found {
		return "", "", false
	}
	var spec skillSpec
	_ = json.Unmarshal([]byte(a.Spec), &spec)
	return spec.Content, a.UpdatedAt, true
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
	return RepoSkill{ID: a.ID, Name: a.Name, Content: spec.Content, RepoID: a.RepoID, Scope: a.Scope, AutoAll: spec.AutoAll}
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
