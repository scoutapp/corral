// Package automations is Corral's action + trigger + run engine. It turns
// hard-coded event bodies (PR approve/comment/merge, "Analyze with AI", project
// start, …) into user-configurable units of work.
//
// The pieces, bottom-up:
//   - Action  — a reusable, typed unit of work (this file: the data model).
//   - Runner  — executes an action and records the result (runner.go).
//   - Hook    — binds an event to an action|flow (hooks.go).
//   - Flow    — a composed, ordered list of steps over actions.
//
// This file is the store-backed data layer: the domain types plus CRUD against
// the shared corral.db (tables created by migration 0007_automations.sql). It
// deliberately holds no execution logic — that lives in the runner — so the
// model stays a plain, testable persistence layer.
package automations

import (
	"database/sql"
	"fmt"

	"github.com/scoutapp/corral/internal/store"
)

// Scope constants. A global row is the default; a repo-scoped row (repo_id set)
// overrides/augments it for that repo.
const (
	ScopeGlobal = "global"
	ScopeRepo   = "repo"
)

// Action kinds. Capability actions are provider-agnostic (the concrete driver —
// GitHub via gh — is chosen in Go); the rest are self-describing.
const (
	KindCapability   = "capability"    // pr-approve, pr-comment, pr-merge, …
	KindBash         = "bash"          // raw script, sandboxed
	KindClaudePrompt = "claude_prompt" // a prompt template (project-start, etc.)
	KindWebhook      = "webhook"       // HTTP POST
	KindSlack        = "slack"         // Slack message
)

// Hookable events. Kept as constants so the UI, the resolver, and the emit
// sites all agree on the same wire strings.
const (
	EventPRApprove        = "pr.approve"
	EventPRComment        = "pr.comment"
	EventPRRequestChanges = "pr.request_changes"
	EventPRMerge          = "pr.merge"
	EventPRAnalyze        = "pr.analyze"
	EventPREnter          = "pr.enter"
	EventProjectStart     = "project.start"
)

// Action is a reusable unit of work. spec_json holds per-kind typed config
// (kept as a raw string here; the runner decodes it per kind).
type Action struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Spec      string `json:"spec"` // raw JSON; shape depends on Kind
	Scope     string `json:"scope"`
	RepoID    string `json:"repoId,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Hook binds an event to an action or flow, ordered by Position within its
// (event, scope, repo) group. Disabled hooks are skipped by the resolver.
type Hook struct {
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	Scope      string `json:"scope"`
	RepoID     string `json:"repoId,omitempty"`
	TargetKind string `json:"targetKind"` // action | flow
	TargetID   int64  `json:"targetId"`
	Position   int    `json:"position"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"createdAt"`
}

// Service is the store-backed data layer for automations. Like prreview.Service
// it holds only the shared *sql.DB and is safe for concurrent use.
type Service struct {
	db *sql.DB
}

// New wraps the shared store.
func New(s *store.Store) *Service {
	return &Service{db: s.DB()}
}

// --- Actions ---------------------------------------------------------------

// CreateAction inserts an action and returns it with its assigned ID and
// timestamps. A blank spec is normalized to "{}" so callers never store NULL.
func (s *Service) CreateAction(a Action) (Action, error) {
	if a.Spec == "" {
		a.Spec = "{}"
	}
	if a.Scope == "" {
		a.Scope = ScopeGlobal
	}
	res, err := s.db.Exec(`
		INSERT INTO auto_actions (name, kind, spec_json, scope, repo_id)
		VALUES (?, ?, ?, ?, ?)
	`, a.Name, a.Kind, a.Spec, a.Scope, nullIf(a.RepoID))
	if err != nil {
		return Action{}, fmt.Errorf("automations: create action: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.Action(id)
}

// Action fetches one action by ID.
func (s *Service) Action(id int64) (Action, error) {
	var a Action
	var repoID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, name, kind, spec_json, scope, repo_id, created_at, updated_at
		  FROM auto_actions WHERE id = ?
	`, id).Scan(&a.ID, &a.Name, &a.Kind, &a.Spec, &a.Scope, &repoID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Action{}, err
	}
	a.RepoID = repoID.String
	return a, nil
}

// ListActions returns actions filtered by scope. When repoID is non-empty it
// returns that repo's actions PLUS the global ones (the repo sees both its own
// and the shared catalog); when empty it returns only global actions.
func (s *Service) ListActions(repoID string) ([]Action, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if repoID == "" {
		rows, err = s.db.Query(`
			SELECT id, name, kind, spec_json, scope, repo_id, created_at, updated_at
			  FROM auto_actions WHERE scope = 'global' ORDER BY name
		`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, name, kind, spec_json, scope, repo_id, created_at, updated_at
			  FROM auto_actions
			 WHERE scope = 'global' OR repo_id = ?
			 ORDER BY (scope = 'global') DESC, name
		`, repoID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActions(rows)
}

// UpdateAction updates a mutable action's name and spec, bumping updated_at.
func (s *Service) UpdateAction(id int64, name, spec string) (Action, error) {
	if spec == "" {
		spec = "{}"
	}
	_, err := s.db.Exec(`
		UPDATE auto_actions SET name = ?, spec_json = ?, updated_at = datetime('now')
		 WHERE id = ?
	`, name, spec, id)
	if err != nil {
		return Action{}, fmt.Errorf("automations: update action: %w", err)
	}
	return s.Action(id)
}

// DeleteAction removes an action (cascading to flow steps and leaving hooks
// dangling by target_id — callers should clean up hooks that referenced it).
func (s *Service) DeleteAction(id int64) error {
	_, err := s.db.Exec(`DELETE FROM auto_actions WHERE id = ?`, id)
	return err
}

// --- Hooks -----------------------------------------------------------------

// CreateHook binds an event to an action|flow.
func (s *Service) CreateHook(h Hook) (Hook, error) {
	if h.Scope == "" {
		h.Scope = ScopeGlobal
	}
	res, err := s.db.Exec(`
		INSERT INTO auto_hooks (event, scope, repo_id, target_kind, target_id, position, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, h.Event, h.Scope, nullIf(h.RepoID), h.TargetKind, h.TargetID, h.Position, boolToInt(h.Enabled))
	if err != nil {
		return Hook{}, fmt.Errorf("automations: create hook: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.hook(id)
}

func (s *Service) hook(id int64) (Hook, error) {
	var h Hook
	var repoID sql.NullString
	var enabled int
	err := s.db.QueryRow(`
		SELECT id, event, scope, repo_id, target_kind, target_id, position, enabled, created_at
		  FROM auto_hooks WHERE id = ?
	`, id).Scan(&h.ID, &h.Event, &h.Scope, &repoID, &h.TargetKind, &h.TargetID, &h.Position, &enabled, &h.CreatedAt)
	if err != nil {
		return Hook{}, err
	}
	h.RepoID = repoID.String
	h.Enabled = enabled != 0
	return h, nil
}

// HooksForEvent returns the enabled hooks bound to an event that apply to a
// repo: global hooks plus that repo's own, ordered by scope (global first) then
// position. This ordering is what the resolver replays. Passing an empty repoID
// returns only global hooks.
func (s *Service) HooksForEvent(event, repoID string) ([]Hook, error) {
	rows, err := s.db.Query(`
		SELECT id, event, scope, repo_id, target_kind, target_id, position, enabled, created_at
		  FROM auto_hooks
		 WHERE event = ? AND enabled = 1
		   AND (scope = 'global' OR repo_id = ?)
		 ORDER BY (scope = 'global') DESC, position, id
	`, event, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hook{}
	for rows.Next() {
		var h Hook
		var rid sql.NullString
		var enabled int
		if err := rows.Scan(&h.ID, &h.Event, &h.Scope, &rid, &h.TargetKind, &h.TargetID, &h.Position, &enabled, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.RepoID = rid.String
		h.Enabled = enabled != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteHook removes a hook binding.
func (s *Service) DeleteHook(id int64) error {
	_, err := s.db.Exec(`DELETE FROM auto_hooks WHERE id = ?`, id)
	return err
}

// --- helpers ---------------------------------------------------------------

func scanActions(rows *sql.Rows) ([]Action, error) {
	out := []Action{}
	for rows.Next() {
		var a Action
		var repoID sql.NullString
		if err := rows.Scan(&a.ID, &a.Name, &a.Kind, &a.Spec, &a.Scope, &repoID, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.RepoID = repoID.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// nullIf maps an empty string to a SQL NULL so global rows store NULL repo_id
// (and the scope indexes stay meaningful) rather than "".
func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
