// Package applog is Corral's central application log: one host-wide, searchable
// record of everything the app does or runs. Every operation worth surfacing
// (AI analysis, PR actions, project lifecycle, automation runs, scripts, HTTP
// requests) emits a structured row here through a single Logger, backed by the
// shared corral.db (table app_logs, migration 0008). The Logs tab reads it back
// with keyset pagination + FTS5 search; a retention job prunes it.
//
// Writes are BEST-EFFORT: a logging failure must never break the operation being
// logged. Errors from Log are swallowed (optionally mirrored to stderr in dev).
package applog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/scoutapp/corral/internal/store"
)

// Levels.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Categories — the coarse buckets the Logs UI filters by.
const (
	CatAI         = "ai"
	CatPRAction   = "pr-action"
	CatProject    = "project"
	CatAutomation = "automation"
	CatScript     = "script"
	CatRepo       = "repo"
	CatChat       = "chat"
	CatHTTP       = "http"
	CatSystem     = "system"
)

// Status values (for timed/outcome-bearing operations; empty for pure info).
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusPartial = "partial"
)

// Entry is one log record. Only Level/Category/Event/Message are required.
type Entry struct {
	Level      string
	Category   string
	Event      string
	Message    string
	RepoID     string
	ProjectID  string
	Status     string
	DurationMs int64
	Meta       map[string]any
	RunID      int64

	// Tracing (migration 0009). Usually set for you by StartSpan/LogCtx from the
	// context carrier rather than filled by hand. TraceID groups an action's
	// whole causal tree; SpanID identifies this span (shared by its .start/.end
	// rows); ParentSpanID points at the enclosing span (empty at the root).
	TraceID      string
	SpanID       string
	ParentSpanID string
}

// Logger writes entries to app_logs. Safe for concurrent use (database/sql
// pools). Construct once with New and share it; a nil Logger is a valid no-op
// so call sites that don't have a logger wired can still call methods safely.
type Logger struct {
	db      *sql.DB
	debug   bool // when false, LevelDebug entries are dropped
	onError func(error)
}

// New wraps the shared store. debug enables persistence of debug-level entries.
func New(s *store.Store, debug bool) *Logger {
	return &Logger{db: s.DB(), debug: debug}
}

// Log records an entry (best-effort). Nil-safe; drops debug entries unless debug
// is enabled. Never returns an error — logging must not fail the caller.
func (l *Logger) Log(e Entry) {
	if l == nil || l.db == nil {
		return
	}
	if e.Level == LevelDebug && !l.debug {
		return
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}
	meta := "{}"
	if len(e.Meta) > 0 {
		if b, err := json.Marshal(e.Meta); err == nil {
			meta = string(b)
		}
	}
	var runID any
	if e.RunID != 0 {
		runID = e.RunID
	}
	_, err := l.db.Exec(`
		INSERT INTO app_logs (level, category, event, message, repo_id, project_id, status, duration_ms, meta_json, run_id, trace_id, span_id, parent_span_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.Level, e.Category, e.Event, e.Message,
		nullIf(e.RepoID), nullIf(e.ProjectID), nullIf(e.Status),
		nullIfZero(e.DurationMs), meta, runID,
		nullIf(e.TraceID), nullIf(e.SpanID), nullIf(e.ParentSpanID))
	if err != nil && l.onError != nil {
		l.onError(err)
	}
}

// Info logs an info-level entry with optional structured meta.
func (l *Logger) Info(category, event, message string, meta map[string]any) {
	l.Log(Entry{Level: LevelInfo, Category: category, Event: event, Message: message, Meta: meta})
}

// Errorf logs an error-level entry, folding the error text into meta.error and
// setting status=error.
func (l *Logger) Errorf(category, event, message string, err error, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	if err != nil {
		meta["error"] = err.Error()
	}
	l.Log(Entry{Level: LevelError, Category: category, Event: event, Message: message, Status: StatusError, Meta: meta})
}

// Time runs fn, logs the outcome (ok/error) with duration_ms, and returns fn's
// error. The message is used as-is; on error the error text lands in meta.error.
// A convenient wrapper for timed operations (AI calls, scripts, requests).
func (l *Logger) Time(e Entry, fn func() error) error {
	start := time.Now()
	err := fn()
	e.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		e.Status = StatusError
		e.Level = LevelError
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		e.Meta["error"] = err.Error()
	} else if e.Status == "" {
		e.Status = StatusOK
	}
	l.Log(e)
	return err
}

// Fmt is a tiny helper so call sites can build a message inline.
func Fmt(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
