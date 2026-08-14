package automations

import (
	"context"
	"encoding/json"
)

// Hook-chain execution (layer 3). An event ("pr.approve", …) fires a chain: a
// PRIMARY unit of work followed by any user-configured hooks bound to that
// event. The semantics (the Q1 decision):
//
//   - The PRIMARY action determines the operation's outcome. If it fails, the
//     whole operation fails — the caller surfaces that error to the user.
//   - SECONDARY actions (the hooks) are best-effort: each runs, its result is
//     recorded, but a secondary failure never blocks the primary or the others.
//     A chain whose primary succeeded but a secondary failed is StatusPartial.
//
// This is what lets "Approve" keep working exactly as before (the built-in
// gh-approve is the primary) while a user's Slack-notify hook is an additive,
// non-blocking step. The whole chain is recorded as ONE run so the log shows the
// primary result plus every secondary outcome together.

// ChainResult is the outcome of firing an event chain.
type ChainResult struct {
	Status  string       `json:"status"`  // ok | error | partial
	Primary StepResult   `json:"primary"` // the built-in action's result
	Hooks   []StepResult `json:"hooks"`   // secondary results, in order
	RunID   int64        `json:"runId"`
}

// PrimaryFailed reports whether the operation should be treated as failed
// (i.e. surface an error to the user). Only the primary matters.
func (c ChainResult) PrimaryFailed() bool { return c.Primary.Status == StatusError }

// FireEvent runs an event chain: it executes the primary action, then every
// enabled hook bound to the event for the repo (global + repo-scoped, in
// resolution order), recording the whole thing as one run.
//
// primary is the action ID of the built-in behavior (e.g. the gh-approve action
// for pr.approve). Callers that have no distinct built-in — pure notification
// events — may pass 0, in which case all bound hooks are treated as secondary
// and the chain status is ok unless a hook is somehow marked primary (none are).
func (r *Runner) FireEvent(ctx context.Context, event string, primary int64, rc RunContext) (ChainResult, error) {
	rc.Event = event

	runID, err := r.startRun(TriggerHook, rc, "event", 0)
	if err != nil {
		return ChainResult{}, err
	}

	var (
		result ChainResult
		steps  []StepResult
	)

	// Primary first (if any). Its status drives the operation outcome.
	if primary != 0 {
		a, err := r.svc.Action(primary)
		if err != nil {
			// A misconfigured primary is an engine error for the operation.
			ps := StepResult{ActionID: primary, Status: StatusError, Err: "primary action not found"}
			result.Primary = ps
			steps = append(steps, ps)
		} else {
			result.Primary = r.execute(ctx, a, rc)
			steps = append(steps, result.Primary)
		}
	}

	// Secondary hooks — best-effort, recorded but non-blocking.
	hooks, err := r.svc.HooksForEvent(event, rc.RepoID)
	if err == nil {
		for _, h := range hooks {
			if h.TargetKind != "action" {
				continue // flows handled in a later branch
			}
			a, aerr := r.svc.Action(h.TargetID)
			if aerr != nil {
				continue
			}
			hs := r.execute(ctx, a, rc)
			result.Hooks = append(result.Hooks, hs)
			steps = append(steps, hs)
		}
	}

	result.Status = chainStatus(result)
	result.RunID = runID
	if ferr := r.finishRun(runID, result.Status, steps); ferr != nil {
		return result, ferr
	}
	return result, nil
}

// FireSecondary runs ONLY the hooks bound to an event — no primary — recording
// them as one best-effort run. It's for callers that already performed the
// built-in behavior directly (e.g. the existing gh PR-action handlers) and just
// want to fire the user's additive hooks afterward without re-implementing the
// primary as a capability action. The returned result has an empty Primary; its
// status is ok (all secondaries ok / none bound) or partial (a secondary
// failed). It returns (result, nil) even when a secondary fails, since
// secondaries are best-effort — the caller's operation already succeeded.
//
// If no hooks are bound, no run is recorded (nothing happened) and the result
// is the zero value with StatusOK — keeping the common "no automations
// configured" path free of empty run rows.
func (r *Runner) FireSecondary(ctx context.Context, event string, rc RunContext) (ChainResult, error) {
	rc.Event = event
	hooks, err := r.svc.HooksForEvent(event, rc.RepoID)
	if err != nil {
		return ChainResult{}, err
	}
	if len(hooks) == 0 {
		return ChainResult{Status: StatusOK}, nil
	}

	runID, err := r.startRun(TriggerHook, rc, "event", 0)
	if err != nil {
		return ChainResult{}, err
	}

	var (
		result ChainResult
		steps  []StepResult
	)
	for _, h := range hooks {
		if h.TargetKind != "action" {
			continue
		}
		a, aerr := r.svc.Action(h.TargetID)
		if aerr != nil {
			continue
		}
		hs := r.execute(ctx, a, rc)
		result.Hooks = append(result.Hooks, hs)
		steps = append(steps, hs)
	}

	result.Status = StatusOK
	for _, h := range result.Hooks {
		if h.Status == StatusError {
			result.Status = StatusPartial
			break
		}
	}
	result.RunID = runID
	if ferr := r.finishRun(runID, result.Status, steps); ferr != nil {
		return result, ferr
	}
	return result, nil
}

// chainStatus folds step results into the chain status:
//   - error   → the primary failed (operation failed)
//   - partial → primary ok, but at least one secondary failed
//   - ok      → everything succeeded
func chainStatus(c ChainResult) string {
	if c.Primary.Status == StatusError {
		return StatusError
	}
	for _, h := range c.Hooks {
		if h.Status == StatusError {
			return StatusPartial
		}
	}
	return StatusOK
}

// MarshalContextVars is a small helper for callers assembling a RunContext from
// typed data: it flattens a map[string]any of simple values to the string vars
// the engine uses. Non-string values are JSON-encoded. Exposed because emit
// sites (branch 09) build contexts from PR structs.
func MarshalContextVars(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch s := v.(type) {
		case string:
			out[k] = s
		default:
			b, _ := json.Marshal(v)
			out[k] = string(b)
		}
	}
	return out
}
