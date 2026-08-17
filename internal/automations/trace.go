package automations

import "context"

// Tracer lets the runner emit trace spans for a flow/action run without coupling
// the automations package to the applog/logging layer. The dashboard injects an
// applog-backed implementation (see the dashboard's automationsTracer); tests and
// callers that don't care about tracing leave it nil, and the runner no-ops.
//
// It mirrors applog.StartSpan's shape: open a span, get back a child context (so
// nested spans link as children) and an end func to close it with an outcome.
type Tracer interface {
	// StartSpan opens a span named event with a human message and optional structured
	// meta, returning a child context and a func to end it (nil err = ok).
	StartSpan(ctx context.Context, category, event, message string, meta map[string]any) (context.Context, func(error))
}

// noopTracer is used when no tracer is wired: StartSpan returns the context
// unchanged and an end func that does nothing.
type noopTracer struct{}

func (noopTracer) StartSpan(ctx context.Context, _, _, _ string, _ map[string]any) (context.Context, func(error)) {
	return ctx, func(error) {}
}

// tracer returns the runner's tracer, or a no-op when none is set.
func (r *Runner) tracer() Tracer {
	if r.trc == nil {
		return noopTracer{}
	}
	return r.trc
}
