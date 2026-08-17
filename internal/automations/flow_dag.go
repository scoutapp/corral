package automations

import (
	"context"
	"fmt"
	"sort"
)

// Flow execution honors the step dependency graph. Steps are run SEQUENTIALLY
// (one at a time), but in DEPENDENCY order rather than strict position order: a
// step waits until every step_key it depends_on has produced output. Independent
// branches still run — just not simultaneously — so run history and traces stay
// linear and easy to read.
//
// depends_on referencing step keys drives the order; position is only the
// tiebreaker among steps that are otherwise ready, giving a stable, deterministic
// sequence. A plain linear flow (no depends_on) runs in exactly position order,
// identical to before.

// orderSteps returns the steps in a valid sequential execution order (Kahn's
// algorithm, position as the tiebreaker). It errors if depends_on names an
// unknown step key or the graph has a cycle — either is a malformed flow that
// should fail loudly rather than run a partial/undefined order.
func orderSteps(steps []FlowStep) ([]FlowStep, error) {
	// Index by step_key. A step with no key can't be depended on, but can still
	// depend on others.
	byKey := map[string]FlowStep{}
	for _, s := range steps {
		if s.StepKey != "" {
			if _, dup := byKey[s.StepKey]; dup {
				return nil, fmt.Errorf("flow has duplicate step key %q", s.StepKey)
			}
			byKey[s.StepKey] = s
		}
	}

	// remaining unmet dependency count per step id; and dependents adjacency.
	remaining := map[int64]int{}
	stepByID := map[int64]FlowStep{}
	for _, s := range steps {
		stepByID[s.ID] = s
		cnt := 0
		for _, dep := range s.DependsOn {
			if _, ok := byKey[dep]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", labelStep(s), dep)
			}
			cnt++
		}
		remaining[s.ID] = cnt
	}

	// ready = steps with no unmet deps, kept position-sorted so ties are stable.
	ready := []FlowStep{}
	for _, s := range steps {
		if remaining[s.ID] == 0 {
			ready = append(ready, s)
		}
	}
	sortByPosition(ready)

	var ordered []FlowStep
	done := map[string]bool{} // step keys that have completed
	for len(ready) > 0 {
		// Take the lowest-position ready step.
		s := ready[0]
		ready = ready[1:]
		ordered = append(ordered, s)
		if s.StepKey != "" {
			done[s.StepKey] = true
		}
		// Any step depending on this key may now be ready.
		newlyReady := []FlowStep{}
		for _, cand := range steps {
			if remaining[cand.ID] <= 0 {
				continue // already ordered or queued
			}
			if s.StepKey != "" && dependsOnKey(cand, s.StepKey) {
				remaining[cand.ID]--
				if remaining[cand.ID] == 0 {
					newlyReady = append(newlyReady, cand)
				}
			}
		}
		if len(newlyReady) > 0 {
			ready = append(ready, newlyReady...)
			sortByPosition(ready)
		}
	}

	if len(ordered) != len(steps) {
		return nil, fmt.Errorf("flow has a dependency cycle among its steps")
	}
	return ordered, nil
}

func dependsOnKey(s FlowStep, key string) bool {
	for _, d := range s.DependsOn {
		if d == key {
			return true
		}
	}
	return false
}

func sortByPosition(s []FlowStep) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Position != s[j].Position {
			return s[i].Position < s[j].Position
		}
		return s[i].ID < s[j].ID
	})
}

func labelStep(s FlowStep) string {
	if s.StepKey != "" {
		return s.StepKey
	}
	return fmt.Sprintf("#%d", s.Position)
}

// runOrderedSteps executes a flow's steps in dependency order, threading each
// step's output to later steps as {{steps.<key>.output}} and stopping at the
// first failure. Shared by RunFlow and the hook-chain flow path so both honor the
// DAG identically. Returns the step results and the terminal status.
func (r *Runner) runOrderedSteps(ctx context.Context, steps []FlowStep, rc RunContext) ([]StepResult, string) {
	ordered, oerr := orderSteps(steps)
	if oerr != nil {
		return []StepResult{{Status: StatusError, Err: oerr.Error()}}, StatusError
	}

	vars := map[string]string{}
	for k, v := range rc.Vars {
		vars[k] = v
	}

	var out []StepResult
	status := StatusOK
	for _, step := range ordered {
		a, aerr := r.svc.Action(step.ActionID)
		if aerr != nil {
			out = append(out, StepResult{ActionID: step.ActionID, Status: StatusError, Err: "step action not found"})
			status = StatusError
			break
		}
		// Each step is a child span under the flow span, so the trace shows the
		// per-step waterfall.
		stepCtx, endStep := r.tracer().StartSpan(ctx, "automation", "flow.step",
			"Step "+labelStep(step)+" ("+a.Kind+")",
			map[string]any{"step": step.StepKey, "kind": a.Kind, "action": a.Name})
		sr := r.execute(stepCtx, a, RunContext{Event: rc.Event, RepoID: rc.RepoID, Vars: vars})
		if sr.Status == StatusError {
			endStep(errFlowFailed)
		} else {
			endStep(nil)
		}
		out = append(out, sr)
		if sr.Status == StatusError {
			status = StatusError
			break
		}
		if step.StepKey != "" {
			vars["steps."+step.StepKey+".output"] = sr.Output
		}
	}
	return out, status
}
