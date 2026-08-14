package automations

import (
	"context"
	"encoding/json"
	"fmt"
)

// Flow execution: run a flow's steps in order, threading a variable bag so a
// later step can reference an earlier one's output as {{steps.<key>.output}}.
// Execution is LINEAR today; the data model (step_key + depends_on) is already
// DAG-ready, so branching/parallel execution is an additive change here, not a
// schema change.

// FlowResult is the outcome of running a flow.
type FlowResult struct {
	Status string       `json:"status"` // ok | error
	Steps  []StepResult `json:"steps"`
	RunID  int64        `json:"runId"`
}

// RunFlow executes flow `flowID` against a base context, recording one run. Each
// step's output is injected into the context as steps.<key>.output BEFORE the
// next step runs, so templates can chain results. A step that errors STOPS the
// flow (linear semantics: a flow is a pipeline, not a best-effort fan-out) and
// the run is marked error. Returns the per-step results.
func (r *Runner) RunFlow(ctx context.Context, flowID int64, trigger string, rc RunContext) (FlowResult, error) {
	flow, err := r.svc.Flow(flowID)
	if err != nil {
		return FlowResult{}, fmt.Errorf("automations: run flow: load %d: %w", flowID, err)
	}

	runID, err := r.startRun(trigger, rc, "flow", flowID)
	if err != nil {
		return FlowResult{}, err
	}

	// Work on a copy of the vars so step outputs accumulate without mutating the
	// caller's map.
	vars := map[string]string{}
	for k, v := range rc.Vars {
		vars[k] = v
	}

	var (
		result FlowResult
		steps  []StepResult
	)
	for _, step := range flow.Steps {
		a, aerr := r.svc.Action(step.ActionID)
		if aerr != nil {
			sr := StepResult{ActionID: step.ActionID, Status: StatusError, Err: "step action not found"}
			steps = append(steps, sr)
			result.Status = StatusError
			break
		}
		sr := r.execute(ctx, a, RunContext{Event: rc.Event, RepoID: rc.RepoID, Vars: vars})
		steps = append(steps, sr)
		if sr.Status == StatusError {
			result.Status = StatusError
			break // pipeline stops at the first failure
		}
		// Expose this step's output to later steps.
		if step.StepKey != "" {
			vars["steps."+step.StepKey+".output"] = sr.Output
		}
	}

	if result.Status == "" {
		result.Status = StatusOK
	}
	result.Steps = steps
	result.RunID = runID
	if ferr := r.finishRun(runID, result.Status, steps); ferr != nil {
		return result, ferr
	}
	return result, nil
}

// --- small JSON-array helpers (shared by flow steps) -----------------------

func jsonArray(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(xs)
	return string(b)
}

func parseJSONArray(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
