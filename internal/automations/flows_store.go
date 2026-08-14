package automations

import (
	"database/sql"
	"fmt"
)

// Flow persistence — the data layer for composed units of work. The tables
// (auto_flows, auto_flow_steps) exist from migration 0007; this adds the CRUD.
// Steps are ordered by position and graph-ready (StepKey + DependsOn), though
// execution is linear for now.

// Flow is a named, ordered sequence of steps over actions.
type Flow struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Scope   string     `json:"scope"`
	RepoID  string     `json:"repoId,omitempty"`
	Steps   []FlowStep `json:"steps,omitempty"`
	Created string     `json:"createdAt,omitempty"`
}

// FlowStep binds one action into a flow at a position. StepKey is the stable
// handle later steps reference as {{steps.<key>.output}}; DependsOn is carried
// for future DAG execution (unused while linear).
type FlowStep struct {
	ID        int64    `json:"id"`
	FlowID    int64    `json:"flowId"`
	Position  int      `json:"position"`
	ActionID  int64    `json:"actionId"`
	StepKey   string   `json:"stepKey"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// CreateFlow inserts a flow (without steps) and returns it.
func (s *Service) CreateFlow(f Flow) (Flow, error) {
	if f.Scope == "" {
		f.Scope = ScopeGlobal
	}
	res, err := s.db.Exec(`
		INSERT INTO auto_flows (name, scope, repo_id) VALUES (?, ?, ?)
	`, f.Name, f.Scope, nullIf(f.RepoID))
	if err != nil {
		return Flow{}, fmt.Errorf("automations: create flow: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.Flow(id)
}

// AddStep appends an action to a flow at the given position.
func (s *Service) AddStep(step FlowStep) (FlowStep, error) {
	dep := jsonArray(step.DependsOn)
	res, err := s.db.Exec(`
		INSERT INTO auto_flow_steps (flow_id, position, action_id, step_key, depends_on_json)
		VALUES (?, ?, ?, ?, ?)
	`, step.FlowID, step.Position, step.ActionID, step.StepKey, dep)
	if err != nil {
		return FlowStep{}, fmt.Errorf("automations: add step: %w", err)
	}
	id, _ := res.LastInsertId()
	step.ID = id
	return step, nil
}

// Flow fetches a flow with its steps, ordered by position.
func (s *Service) Flow(id int64) (Flow, error) {
	var f Flow
	var repoID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, name, scope, repo_id, created_at FROM auto_flows WHERE id = ?
	`, id).Scan(&f.ID, &f.Name, &f.Scope, &repoID, &f.Created)
	if err != nil {
		return Flow{}, err
	}
	f.RepoID = repoID.String

	rows, err := s.db.Query(`
		SELECT id, flow_id, position, action_id, step_key, depends_on_json
		  FROM auto_flow_steps WHERE flow_id = ? ORDER BY position, id
	`, id)
	if err != nil {
		return Flow{}, err
	}
	defer rows.Close()
	f.Steps = []FlowStep{}
	for rows.Next() {
		var st FlowStep
		var dep string
		if err := rows.Scan(&st.ID, &st.FlowID, &st.Position, &st.ActionID, &st.StepKey, &dep); err != nil {
			return Flow{}, err
		}
		st.DependsOn = parseJSONArray(dep)
		f.Steps = append(f.Steps, st)
	}
	return f, rows.Err()
}

// ListFlows returns flows visible to a repo (its own + global), or just global
// when repoID is empty. Steps are not loaded (call Flow for the full object).
func (s *Service) ListFlows(repoID string) ([]Flow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if repoID == "" {
		rows, err = s.db.Query(`SELECT id, name, scope, repo_id, created_at FROM auto_flows WHERE scope = 'global' ORDER BY name`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, name, scope, repo_id, created_at FROM auto_flows
			 WHERE scope = 'global' OR repo_id = ? ORDER BY (scope = 'global') DESC, name
		`, repoID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flow{}
	for rows.Next() {
		var f Flow
		var repoID sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &f.Scope, &repoID, &f.Created); err != nil {
			return nil, err
		}
		f.RepoID = repoID.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFlow removes a flow (steps cascade via FK).
func (s *Service) DeleteFlow(id int64) error {
	_, err := s.db.Exec(`DELETE FROM auto_flows WHERE id = ?`, id)
	return err
}
