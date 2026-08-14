package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// The runner is layer 1 of the engine: the single place that executes an action
// and records the result. Everything that runs a unit of work — hooks, the
// /api action:run endpoint, later flows and the CLI — goes through Run so that
// logging, timing, and the append-only auto_runs history are uniform.
//
// Execution itself is delegated to an Executor chosen by the action's Kind.
// This keeps the runner agnostic to *what* an action does: branch 03 registers
// capability/provider executors, branch 11 registers the bash executor, etc.,
// without touching the runner.

// RunContext is the input bag handed to an action: the event that triggered it
// (empty for manual/api runs), the repo, and a free-form map of variables
// (pr number, url, actor, …). Executors read from Vars; bash/prompt actions
// substitute them, capability drivers pull typed fields out.
type RunContext struct {
	Event  string            `json:"event,omitempty"`
	RepoID string            `json:"repoId,omitempty"`
	Vars   map[string]string `json:"vars,omitempty"`
}

// Var returns a context variable or "" if unset. Nil-safe.
func (c RunContext) Var(key string) string {
	if c.Vars == nil {
		return ""
	}
	return c.Vars[key]
}

// StepResult is the outcome of executing one action. Output is the primary
// textual result (stdout for commands, the message for a prompt); Err is a
// human-readable failure reason when Status is StatusError.
type StepResult struct {
	ActionID int64  `json:"actionId"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Status   string `json:"status"` // ok | error
	Output   string `json:"output,omitempty"`
	Err      string `json:"error,omitempty"`
	Duration int64  `json:"durationMs"`
}

// Run status values (also used for auto_runs.status).
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusRunning = "running"
	StatusPartial = "partial" // a flow/hook chain where some non-primary step failed
)

// Trigger values for auto_runs.trigger.
const (
	TriggerManual = "manual"
	TriggerAPI    = "api"
	TriggerHook   = "hook"
	TriggerFlow   = "flow-step"
)

// Executor runs one action of a given Kind against a RunContext. Implementations
// must be safe for concurrent use. An Executor returns a StepResult it fully
// populates except Duration (the runner times it) — and returns an error only
// for *engine* failures (misconfiguration, decode errors); an action that runs
// but fails (e.g. gh returns non-zero) is reported via StepResult.Status=error,
// not a returned error, so the run is still recorded.
type Executor interface {
	Execute(ctx context.Context, a Action, rc RunContext) StepResult
}

// Registry maps an action Kind to its Executor. The runner holds one.
type Registry struct {
	byKind map[string]Executor
}

// NewRegistry returns an empty registry. Callers Register executors per kind.
func NewRegistry() *Registry {
	return &Registry{byKind: map[string]Executor{}}
}

// Register binds an Executor to a kind, replacing any previous one.
func (r *Registry) Register(kind string, e Executor) {
	r.byKind[kind] = e
}

// executorFor returns the executor for a kind, or nil.
func (r *Registry) executorFor(kind string) Executor {
	return r.byKind[kind]
}

// Runner executes actions and records runs. It combines the store Service (to
// persist auto_runs and to look up actions) with a Registry of executors.
type Runner struct {
	svc *Service
	reg *Registry
	now func() time.Time // injectable for tests
}

// NewRunner wires a runner. now defaults to time.Now.
func NewRunner(svc *Service, reg *Registry) *Runner {
	return &Runner{svc: svc, reg: reg, now: time.Now}
}

// RunAction executes a single action by ID and records the run. It returns the
// StepResult; a run row is always written (even on executor-missing / failure)
// so the history is complete.
func (r *Runner) RunAction(ctx context.Context, actionID int64, trigger string, rc RunContext) (StepResult, error) {
	a, err := r.svc.Action(actionID)
	if err != nil {
		return StepResult{}, fmt.Errorf("automations: run: load action %d: %w", actionID, err)
	}

	runID, err := r.startRun(trigger, rc, "action", a.ID)
	if err != nil {
		return StepResult{}, err
	}

	res := r.execute(ctx, a, rc)

	status := res.Status
	if err := r.finishRun(runID, status, []StepResult{res}); err != nil {
		return res, err
	}
	return res, nil
}

// execute times and dispatches one action to its executor.
func (r *Runner) execute(ctx context.Context, a Action, rc RunContext) StepResult {
	start := r.now()
	exec := r.reg.executorFor(a.Kind)
	if exec == nil {
		return StepResult{
			ActionID: a.ID, Kind: a.Kind, Name: a.Name,
			Status: StatusError,
			Err:    fmt.Sprintf("no executor registered for kind %q", a.Kind),
			Duration: r.now().Sub(start).Milliseconds(),
		}
	}
	res := exec.Execute(ctx, a, rc)
	res.ActionID, res.Kind, res.Name = a.ID, a.Kind, a.Name
	if res.Status == "" {
		res.Status = StatusOK
	}
	res.Duration = r.now().Sub(start).Milliseconds()
	return res
}

// startRun inserts a running auto_runs row and returns its ID.
func (r *Runner) startRun(trigger string, rc RunContext, targetKind string, targetID int64) (int64, error) {
	ctxJSON, _ := json.Marshal(rc)
	res, err := r.svc.db.Exec(`
		INSERT INTO auto_runs (trigger, event, target_kind, target_id, status, context_json, steps_json)
		VALUES (?, ?, ?, ?, ?, ?, '[]')
	`, trigger, nullIf(rc.Event), targetKind, targetID, StatusRunning, string(ctxJSON))
	if err != nil {
		return 0, fmt.Errorf("automations: start run: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// finishRun stamps the terminal status, step results, and finished_at.
func (r *Runner) finishRun(runID int64, status string, steps []StepResult) error {
	stepsJSON, _ := json.Marshal(steps)
	_, err := r.svc.db.Exec(`
		UPDATE auto_runs SET status = ?, steps_json = ?, finished_at = datetime('now')
		 WHERE id = ?
	`, status, string(stepsJSON), runID)
	if err != nil {
		return fmt.Errorf("automations: finish run: %w", err)
	}
	return nil
}
