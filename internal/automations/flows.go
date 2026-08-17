package automations

import (
	"context"
	"encoding/json"
	"fmt"
)

// Flow execution: run a flow's steps in DEPENDENCY order (see flow_dag.go),
// threading a variable bag so a later step can reference an earlier one's output
// as {{steps.<key>.output}}. Steps run sequentially; a step waits for the steps
// it depends_on. A plain linear flow (no depends_on) runs in position order,
// exactly as before.

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

	// Run the steps in dependency order (honors depends_on; sequential).
	steps, status := r.runOrderedSteps(ctx, flow.Steps, rc)

	result := FlowResult{Status: status, Steps: steps, RunID: runID}
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
