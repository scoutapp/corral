package applog

import (
	"context"
	"time"
)

// A span is a timed unit of work recorded as two app_logs rows sharing one
// span_id: a `.start` row at T0 and a `.end` row at T1 with the final status.
// StartSpan emits the start row and returns a child context (carrying this span
// as the parent for anything it calls) plus an End function to close it.
//
// Typical use — the whole body is bracketed so the end row always fires:
//
//	ctx, end := logger.StartSpan(ctx, applog.Entry{
//	    Category: applog.CatAI, Event: "ai.call", Message: "risk analysis",
//	})
//	err := doTheWork(ctx)          // nested spans inherit trace + parent here
//	end(err)                        // status derived from err (ok / error)
//
// A root action passes context.Background() (or the request context): StartSpan
// mints a fresh trace_id when the context carries none.

// SpanEnd closes a span, emitting its `.end` row. Pass the operation's error (nil
// for success); status is derived (ok/error) unless already set on the closing
// entry. Safe to call on a nil Logger.
type SpanEnd func(err error)

// StartSpan opens a span. It:
//   - inherits trace_id from ctx, or mints one if ctx is untraced (making this a
//     root span);
//   - allocates a span_id and records parent_span_id = the span currently open in
//     ctx;
//   - emits the `.start` row immediately (event = e.Event + ".start");
//   - returns a child context carrying (trace_id, this span_id) and a SpanEnd
//     that stamps duration_ms and emits the `.end` row.
//
// Nil-safe: a nil Logger still returns a usable (trace-carrying) context and a
// no-op end, so callers never have to nil-check before tracing.
func (l *Logger) StartSpan(ctx context.Context, e Entry) (context.Context, SpanEnd) {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID := TraceID(ctx)
	if traceID == "" {
		traceID = newID()
	}
	spanID := newID()
	parent := SpanID(ctx)

	// Child context: same trace, this span becomes the parent for descendants.
	child := context.WithValue(ctx, traceKey{}, traceCtx{traceID: traceID, spanID: spanID})

	start := time.Now()

	// Emit the start row. Copy the entry so we don't mutate the caller's.
	s := e
	s.TraceID = traceID
	s.SpanID = spanID
	s.ParentSpanID = parent
	s.Event = e.Event + ".start"
	if s.Level == "" {
		s.Level = LevelInfo
	}
	s.Status = "" // a start has no outcome yet
	s.DurationMs = 0
	l.Log(s)

	end := func(err error) {
		en := e
		en.TraceID = traceID
		en.SpanID = spanID
		en.ParentSpanID = parent
		en.Event = e.Event + ".end"
		en.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			en.Status = StatusError
			en.Level = LevelError
			if en.Meta == nil {
				en.Meta = map[string]any{}
			}
			en.Meta["error"] = err.Error()
		} else if en.Status == "" {
			en.Status = StatusOK
		}
		l.Log(en)
	}
	return child, end
}

// LogCtx logs a point-in-time entry placed inside the trace carried by ctx (it
// inherits trace_id and sets parent_span_id to the open span). Use it for
// notable moments within a span that aren't themselves timed spans.
func (l *Logger) LogCtx(ctx context.Context, e Entry) {
	e.TraceID = TraceID(ctx)
	e.ParentSpanID = SpanID(ctx)
	l.Log(e)
}
