package applog

import (
	"context"
	"errors"
	"testing"

	"github.com/scoutapp/corral/internal/store"
)

func newLogger(t *testing.T, debug bool) *Logger {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	s, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s, debug)
}

func TestLogAndQuery(t *testing.T) {
	l := newLogger(t, false)

	l.Info(CatProject, "project.start", "Started acme/widget", map[string]any{"project": "p1"})
	l.Info(CatAI, "ai.analyze", "Analyzed PR #42 — 6 blocks", map[string]any{"pr": 42})
	l.Errorf(CatPRAction, "pr.merge", "Merge failed", errors.New("not mergeable"), nil)

	page, err := l.Query(Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(page.Logs))
	}
	// Newest-first.
	if page.Logs[0].Event != "pr.merge" {
		t.Errorf("expected newest first, got %q", page.Logs[0].Event)
	}
	// Error entry captured status + error meta.
	if page.Logs[0].Status != StatusError || page.Logs[0].Level != LevelError {
		t.Errorf("error entry not marked: %+v", page.Logs[0])
	}
}

func TestDebugDroppedUnlessEnabled(t *testing.T) {
	l := newLogger(t, false)
	l.Log(Entry{Level: LevelDebug, Category: CatSystem, Event: "x", Message: "verbose"})
	if n, _ := l.Count(); n != 0 {
		t.Errorf("debug entry should be dropped when debug off, got %d", n)
	}

	l2 := newLogger(t, true)
	l2.Log(Entry{Level: LevelDebug, Category: CatSystem, Event: "x", Message: "verbose"})
	if n, _ := l2.Count(); n != 1 {
		t.Errorf("debug entry should persist when debug on, got %d", n)
	}
}

func TestKeysetPagination(t *testing.T) {
	l := newLogger(t, false)
	for i := 0; i < 25; i++ {
		l.Info(CatSystem, "tick", Fmt("event %d", i), nil)
	}
	// First page of 10.
	p1, _ := l.Query(Query{Limit: 10})
	if len(p1.Logs) != 10 || p1.NextCursor == 0 {
		t.Fatalf("page1 wrong: n=%d cursor=%d", len(p1.Logs), p1.NextCursor)
	}
	// Second page via cursor.
	p2, _ := l.Query(Query{Limit: 10, Before: p1.NextCursor})
	if len(p2.Logs) != 10 || p2.NextCursor == 0 {
		t.Fatalf("page2 wrong: n=%d cursor=%d", len(p2.Logs), p2.NextCursor)
	}
	// No overlap: p2's newest id is older than p1's oldest.
	if p2.Logs[0].ID >= p1.Logs[len(p1.Logs)-1].ID {
		t.Error("pages overlap")
	}
	// Last page: 5 left, no further cursor.
	p3, _ := l.Query(Query{Limit: 10, Before: p2.NextCursor})
	if len(p3.Logs) != 5 || p3.NextCursor != 0 {
		t.Fatalf("page3 wrong: n=%d cursor=%d", len(p3.Logs), p3.NextCursor)
	}
}

func TestFiltersAndSearch(t *testing.T) {
	l := newLogger(t, false)
	l.Info(CatAI, "ai.analyze", "Analyzed PR #42 widget", map[string]any{"repo": "acme"})
	l.Info(CatPRAction, "pr.approve", "Approved PR #42", nil)
	l.Log(Entry{Level: LevelInfo, Category: CatProject, Event: "project.start", Message: "Started x", ProjectID: "proj-1"})

	// Category filter.
	if p, _ := l.Query(Query{Category: CatAI, Limit: 10}); len(p.Logs) != 1 || p.Logs[0].Category != CatAI {
		t.Errorf("category filter failed: %+v", p.Logs)
	}
	// Project filter.
	if p, _ := l.Query(Query{Project: "proj-1", Limit: 10}); len(p.Logs) != 1 {
		t.Errorf("project filter failed: %+v", p.Logs)
	}
	// FTS search hits the message.
	if p, _ := l.Query(Query{Q: "widget", Limit: 10}); len(p.Logs) != 1 || p.Logs[0].Event != "ai.analyze" {
		t.Errorf("search 'widget' failed: %+v", p.Logs)
	}
	// Substring search ("approv" matches "Approved").
	if p, _ := l.Query(Query{Q: "approv", Limit: 10}); len(p.Logs) != 1 {
		t.Errorf("substring search failed: %+v", p.Logs)
	}
	// Search hits meta_json too (repo:"acme").
	if p, _ := l.Query(Query{Q: "acme", Limit: 10}); len(p.Logs) != 1 {
		t.Errorf("meta search failed: %+v", p.Logs)
	}
}

func TestPruneByCount(t *testing.T) {
	l := newLogger(t, false)
	for i := 0; i < 20; i++ {
		l.Info(CatSystem, "tick", Fmt("e%d", i), nil)
	}
	deleted, err := l.Prune(Retention{MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 15 {
		t.Errorf("expected 15 pruned, got %d", deleted)
	}
	if n, _ := l.Count(); n != 5 {
		t.Errorf("expected 5 remaining, got %d", n)
	}
	// FTS stayed in sync — search over a pruned message returns nothing extra.
	if p, _ := l.Query(Query{Q: "e0", Limit: 50}); len(p.Logs) != 0 {
		t.Errorf("pruned rows should be gone from FTS too, got %d", len(p.Logs))
	}
}

func TestSpanPairAndNesting(t *testing.T) {
	l := newLogger(t, false)

	// Root span (untraced context → mints a trace) with one nested child.
	ctx, endRoot := l.StartSpan(context.Background(), Entry{
		Category: CatChat, Event: "chat.turn", Message: "triage errors",
	})
	traceID := TraceID(ctx)
	if traceID == "" {
		t.Fatal("root StartSpan did not mint a trace id")
	}
	rootSpan := SpanID(ctx)
	if rootSpan == "" {
		t.Fatal("root context missing span id")
	}

	childCtx, endChild := l.StartSpan(ctx, Entry{
		Category: CatAI, Event: "ai.call", Message: "plan",
	})
	if TraceID(childCtx) != traceID {
		t.Error("child did not inherit trace id")
	}
	endChild(nil)
	endRoot(nil)

	// Read newest-first: root.end, child.end, child.start, root.start.
	page, _ := l.Query(Query{Limit: 10})
	if len(page.Logs) != 4 {
		t.Fatalf("expected 4 span rows, got %d", len(page.Logs))
	}
	// Every row shares the trace id.
	for _, r := range page.Logs {
		if r.TraceID != traceID {
			t.Errorf("row %s has trace %q, want %q", r.Event, r.TraceID, traceID)
		}
	}
	// Find child rows: they carry parent_span_id == rootSpan.
	var childStart, childEnd *Record
	for i := range page.Logs {
		r := &page.Logs[i]
		if r.Event == "ai.call.start" {
			childStart = r
		}
		if r.Event == "ai.call.end" {
			childEnd = r
		}
	}
	if childStart == nil || childEnd == nil {
		t.Fatal("missing child .start/.end rows")
	}
	if childStart.ParentSpanID != rootSpan {
		t.Errorf("child parent_span_id = %q, want root %q", childStart.ParentSpanID, rootSpan)
	}
	// start and end share a span id; end carries ok status.
	if childStart.SpanID != childEnd.SpanID {
		t.Errorf("child start/end span ids differ: %q vs %q", childStart.SpanID, childEnd.SpanID)
	}
	if childEnd.Status != StatusOK {
		t.Errorf("child end status = %q, want ok", childEnd.Status)
	}
}

func TestSpanEndError(t *testing.T) {
	l := newLogger(t, false)
	_, end := l.StartSpan(context.Background(), Entry{Category: CatScript, Event: "script.test", Message: "run"})
	end(errors.New("boom"))

	// The .end row is error-marked with the error in meta.
	p, _ := l.Query(Query{Q: "boom", Limit: 10})
	if len(p.Logs) != 1 || p.Logs[0].Event != "script.test.end" || p.Logs[0].Status != StatusError {
		t.Fatalf("error span end not recorded: %+v", p.Logs)
	}
}

func TestNilLoggerSpanSafe(t *testing.T) {
	var l *Logger
	// StartSpan on a nil logger still returns a trace-carrying ctx + no-op end.
	ctx, end := l.StartSpan(context.Background(), Entry{Category: CatSystem, Event: "x"})
	if TraceID(ctx) == "" {
		t.Error("nil-logger StartSpan should still carry a trace id in ctx")
	}
	end(nil) // must not panic
}

func TestNilLoggerSafe(t *testing.T) {
	var l *Logger
	l.Info(CatSystem, "x", "y", nil) // must not panic
	if p, err := l.Query(Query{}); err != nil || len(p.Logs) != 0 {
		t.Errorf("nil logger query should be empty/no-error")
	}
}
