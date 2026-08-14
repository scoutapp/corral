package applog

import (
	"sort"
	"strconv"
)

// Trace reconstruction: fold the flat app_logs rows of one trace_id back into the
// span tree the viewer draws. Rows come in two shapes (migration 0009):
//   - span pairs: a `.start` row and a `.end` row sharing one span_id — the timed
//     spans (chat turns, AI calls, project starts).
//   - single rows: a point-in-time log (or a single-row root like http.request)
//     that carries trace_id/span_id but no separate start+end.
//
// We reconcile by span_id: start+end collapse into one Span with a duration; a
// lone start is UNTERMINATED (still running, or its end was pruned at the
// retention boundary); a lone end (start pruned) is shown at its own ts. Point
// rows with no matching span become their own zero-duration spans so nothing is
// dropped from the tree.

// Span is one reconciled node of a trace.
type Span struct {
	SpanID       string   `json:"spanId"`
	ParentSpanID string   `json:"parentSpanId,omitempty"`
	Category     string   `json:"category"`
	Event        string   `json:"event"` // base event, with .start/.end stripped
	Message      string   `json:"message"`
	Level        string   `json:"level"`
	Status       string   `json:"status,omitempty"`
	StartTS      string   `json:"startTs"`
	EndTS        string   `json:"endTs,omitempty"`
	DurationMs   int64    `json:"durationMs"`
	Unterminated bool     `json:"unterminated,omitempty"` // start seen, no end
	RepoID       string   `json:"repoId,omitempty"`
	ProjectID    string   `json:"projectId,omitempty"`
	RunID        int64    `json:"runId,omitempty"`
	Meta         string   `json:"meta,omitempty"`
	Children     []*Span  `json:"children,omitempty"`
	StartID      int64    `json:"-"` // lowest row id, for stable ordering
}

// Trace is the reconstructed tree for one trace_id: the root spans (usually one)
// plus flat counts the UI can show without walking the tree.
type Trace struct {
	TraceID   string  `json:"traceId"`
	Roots     []*Span `json:"roots"`
	SpanCount int     `json:"spanCount"`
	RowCount  int     `json:"rowCount"`
}

// Trace fetches every row for traceID (oldest-first) and folds it into a span
// tree. Returns an empty Trace (not an error) for an unknown/absent trace id.
func (l *Logger) Trace(traceID string) (Trace, error) {
	if l == nil || l.db == nil || traceID == "" {
		return Trace{TraceID: traceID, Roots: []*Span{}}, nil
	}
	rows, err := l.db.Query(`
		SELECT a.id, a.ts, a.level, a.category, a.event, a.message,
		       COALESCE(a.status,''), COALESCE(a.duration_ms,0), a.meta_json,
		       COALESCE(a.repo_id,''), COALESCE(a.project_id,''), COALESCE(a.run_id,0),
		       COALESCE(a.span_id,''), COALESCE(a.parent_span_id,'')
		  FROM app_logs a
		 WHERE a.trace_id = ?
		 ORDER BY a.id ASC`, traceID)
	if err != nil {
		return Trace{}, err
	}
	defer rows.Close()

	type raw struct {
		id                          int64
		ts, level, category, event  string
		message, status             string
		duration                    int64
		meta, repo, project         string
		runID                       int64
		spanID, parentSpanID        string
	}
	var all []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.id, &r.ts, &r.level, &r.category, &r.event, &r.message,
			&r.status, &r.duration, &r.meta, &r.repo, &r.project, &r.runID,
			&r.spanID, &r.parentSpanID); err != nil {
			return Trace{}, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return Trace{}, err
	}

	// Fold rows into spans keyed by span_id. Rows with no span_id get a synthetic
	// per-row key so they still appear (a point log emitted outside any span).
	byID := map[string]*Span{}
	order := []string{} // span keys in first-seen order
	get := func(key string) *Span {
		if s, ok := byID[key]; ok {
			return s
		}
		s := &Span{SpanID: key}
		byID[key] = s
		order = append(order, key)
		return s
	}

	for _, r := range all {
		key := r.spanID
		if key == "" {
			key = "row:" + strconv.FormatInt(r.id, 10)
		}
		s := get(key)
		base, isStart, isEnd := splitEvent(r.event)

		// First row to touch a span sets its descriptive fields; the start row
		// (or the earliest row) wins for message/category so the label is stable.
		if s.StartID == 0 || r.id < s.StartID {
			s.StartID = r.id
			s.StartTS = r.ts
			s.Category = r.category
			s.Event = base
			s.Message = r.message
			s.Level = r.level
			s.ParentSpanID = r.parentSpanID
			s.RepoID = r.repo
			s.ProjectID = r.project
			s.RunID = r.runID
			s.Meta = r.meta
		}
		switch {
		case isEnd:
			s.EndTS = r.ts
			s.DurationMs = r.duration
			if r.status != "" {
				s.Status = r.status
			}
			if r.level == LevelError {
				s.Level = LevelError
			}
		case isStart:
			// start carries no outcome; timing comes from the end row.
		default:
			// single (point / root) row: it is both start and end of itself.
			if s.EndTS == "" {
				s.EndTS = r.ts
			}
			if r.duration > 0 {
				s.DurationMs = r.duration
			}
			if r.status != "" {
				s.Status = r.status
			}
		}
	}

	// A span that saw a .start but never an .end is unterminated.
	for _, key := range order {
		s := byID[key]
		if s.EndTS == "" {
			s.Unterminated = true
		}
	}

	// Wire parent/child. A span whose parent isn't in this trace (pruned, or it's
	// genuinely a root) becomes a root.
	var roots []*Span
	for _, key := range order {
		s := byID[key]
		if s.ParentSpanID != "" {
			if p, ok := byID[s.ParentSpanID]; ok && p != s {
				p.Children = append(p.Children, s)
				continue
			}
		}
		roots = append(roots, s)
	}

	// Stable ordering everywhere: by first row id (chronological).
	sortSpans(roots)
	for _, key := range order {
		sortSpans(byID[key].Children)
	}

	return Trace{
		TraceID:   traceID,
		Roots:     roots,
		SpanCount: len(order),
		RowCount:  len(all),
	}, nil
}

// splitEvent strips a trailing .start/.end, returning the base event and which
// half (if any) this row is.
func splitEvent(event string) (base string, isStart, isEnd bool) {
	switch {
	case len(event) > 6 && event[len(event)-6:] == ".start":
		return event[:len(event)-6], true, false
	case len(event) > 4 && event[len(event)-4:] == ".end":
		return event[:len(event)-4], false, true
	default:
		return event, false, false
	}
}

func sortSpans(s []*Span) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].StartID < s[j].StartID })
}
