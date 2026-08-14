package applog

import (
	"context"
	"testing"
)

func TestTraceReconstruction(t *testing.T) {
	l := newLogger(t, false)

	// Build a small tree: chat.turn (root) → ai.call (child) with a point log
	// inside the child.
	ctx, endRoot := l.StartSpan(context.Background(), Entry{
		Category: CatChat, Event: "chat.turn", Message: "triage",
	})
	traceID := TraceID(ctx)

	childCtx, endChild := l.StartSpan(ctx, Entry{
		Category: CatAI, Event: "ai.call", Message: "plan",
	})
	l.LogCtx(childCtx, Entry{Category: CatAI, Event: "ai.note", Message: "tool call", Level: LevelInfo})
	endChild(nil)
	endRoot(nil)

	tr, err := l.Trace(traceID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.TraceID != traceID {
		t.Fatalf("trace id mismatch: %q", tr.TraceID)
	}
	if len(tr.Roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(tr.Roots))
	}
	root := tr.Roots[0]
	if root.Event != "chat.turn" || root.Status != StatusOK {
		t.Errorf("root wrong: event=%q status=%q", root.Event, root.Status)
	}
	if root.Unterminated {
		t.Error("root should be terminated")
	}
	// Root has one child span (ai.call); the ai.note point log is a child of the
	// ai.call span (its parent_span_id is the ai.call span).
	if len(root.Children) != 1 {
		t.Fatalf("expected 1 child under root, got %d", len(root.Children))
	}
	child := root.Children[0]
	if child.Event != "ai.call" {
		t.Errorf("child event = %q, want ai.call", child.Event)
	}
	if len(child.Children) != 1 || child.Children[0].Event != "ai.note" {
		t.Errorf("expected ai.note point log under ai.call, got %+v", child.Children)
	}
	// Span count: chat.turn, ai.call, ai.note = 3.
	if tr.SpanCount != 3 {
		t.Errorf("span count = %d, want 3", tr.SpanCount)
	}
	// Row count: 2 (chat pair) + 2 (ai pair) + 1 (note) = 5.
	if tr.RowCount != 5 {
		t.Errorf("row count = %d, want 5", tr.RowCount)
	}
}

func TestTraceUnterminatedSpan(t *testing.T) {
	l := newLogger(t, false)
	// Open a span but never end it (its end row was "pruned" / still running).
	ctx, _ := l.StartSpan(context.Background(), Entry{Category: CatChat, Event: "chat.turn", Message: "x"})
	traceID := TraceID(ctx)

	tr, _ := l.Trace(traceID)
	if len(tr.Roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(tr.Roots))
	}
	if !tr.Roots[0].Unterminated {
		t.Error("span with start and no end should be unterminated")
	}
}

func TestTraceOrphanParentBecomesRoot(t *testing.T) {
	l := newLogger(t, false)
	// A row whose parent_span_id points at a span NOT in this trace (e.g. its
	// start aged out) must still surface as a root, not vanish.
	l.Log(Entry{
		Category: CatAI, Event: "ai.call.start", Message: "orphan",
		TraceID: "t-orphan", SpanID: "s-child", ParentSpanID: "s-missing-parent",
	})
	l.Log(Entry{
		Category: CatAI, Event: "ai.call.end", Message: "orphan",
		TraceID: "t-orphan", SpanID: "s-child", ParentSpanID: "s-missing-parent",
		Status: StatusOK, DurationMs: 12,
	})

	tr, _ := l.Trace("t-orphan")
	if len(tr.Roots) != 1 {
		t.Fatalf("orphan should surface as a root; got %d roots", len(tr.Roots))
	}
	s := tr.Roots[0]
	if s.Event != "ai.call" || s.DurationMs != 12 || s.Status != StatusOK {
		t.Errorf("orphan span reconciled wrong: %+v", s)
	}
}

func TestTraceEmptyForUnknownID(t *testing.T) {
	l := newLogger(t, false)
	tr, err := l.Trace("nope")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Roots) != 0 || tr.SpanCount != 0 {
		t.Errorf("unknown trace should be empty, got %+v", tr)
	}
}
