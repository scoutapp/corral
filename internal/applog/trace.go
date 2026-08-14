package applog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Tracing lets the Logs tab reconstruct the causal tree behind an action: a chat
// turn calls Claude, Claude calls a flow, the flow starts a sandbox. Each of
// those is a span; every span shares one trace_id and points at its parent_span_id.
//
// The plumbing is a context.Context carrier. A root action opens a trace with
// StartSpan on context.Background() (or the request context); everything it calls
// receives that context and opens child spans against it, inheriting the trace_id
// and pointing parent_span_id at the enclosing span — no manual id threading.

// traceCtx is the immutable carrier stored in a context.Context.
type traceCtx struct {
	traceID string
	spanID  string // the currently-open span; children point parent_span_id here
}

type traceKey struct{}

// WithTrace returns a context seeded with an explicit trace id and no open span.
// Use it when a root id is minted elsewhere (e.g. handed across a boundary);
// normally StartSpan mints the trace for you.
func WithTrace(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceKey{}, traceCtx{traceID: traceID})
}

// WithSpan returns a context carrying both a trace id and an already-open span
// id, so descendants created with StartSpan point their parent_span_id at spanID.
// Used at a root that records its own span row directly (e.g. the HTTP middleware
// logs the request as the root span, then seeds this so handlers nest under it).
func WithSpan(ctx context.Context, traceID, spanID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceKey{}, traceCtx{traceID: traceID, spanID: spanID})
}

// NewTraceID mints a fresh id for callers that record a root span row themselves
// (rather than via StartSpan) and need trace_id/span_id up front.
func NewTraceID() string { return newID() }

// TraceID returns the trace id carried by ctx, or "" if the context is untraced.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if tc, ok := ctx.Value(traceKey{}).(traceCtx); ok {
		return tc.traceID
	}
	return ""
}

// SpanID returns the currently-open span id carried by ctx, or "" if none.
func SpanID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if tc, ok := ctx.Value(traceKey{}).(traceCtx); ok {
		return tc.spanID
	}
	return ""
}

// newID returns a short random hex id (8 bytes / 16 chars). crypto/rand is fine
// here — unlike JS in the workflow sandbox, Go has no ban on randomness, and a
// collision within a retention-capped log is astronomically unlikely.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; degrade to a fixed marker rather
		// than panicking inside best-effort logging.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
