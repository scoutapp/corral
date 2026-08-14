package applog

import (
	"database/sql"
	"strings"
)

// Record is one log row as read back for the API/UI.
type Record struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts"`
	Level      string `json:"level"`
	Category   string `json:"category"`
	Event      string `json:"event"`
	Message    string `json:"message"`
	RepoID     string `json:"repoId,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
	Status     string `json:"status,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Meta       string `json:"meta"` // raw JSON
	RunID      int64  `json:"runId,omitempty"`

	TraceID      string `json:"traceId,omitempty"`
	SpanID       string `json:"spanId,omitempty"`
	ParentSpanID string `json:"parentSpanId,omitempty"`
}

// Query filters a page of logs. Empty fields are ignored. Before is the keyset
// cursor: return rows with id < Before (0 = newest page). Q is a free-text FTS
// query over message + meta.
type Query struct {
	Before   int64
	Limit    int
	Category string
	Project  string
	Repo     string
	Level    string
	Q        string
}

// Page is a keyset page of logs, newest-first, plus the cursor for the next
// (older) page. NextCursor is 0 when there are no older rows.
type Page struct {
	Logs       []Record `json:"logs"`
	NextCursor int64    `json:"nextCursor"`
}

// Query returns a keyset page. It fetches Limit+1 rows to detect whether an
// older page exists, then trims and sets NextCursor to the oldest returned id.
func (l *Logger) Query(q Query) (Page, error) {
	if l == nil || l.db == nil {
		return Page{Logs: []Record{}}, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	var (
		sb    strings.Builder
		args  []any
		where []string
	)
	sb.WriteString(`SELECT a.id, a.ts, a.level, a.category, a.event, a.message,
		COALESCE(a.repo_id,''), COALESCE(a.project_id,''), COALESCE(a.status,''),
		COALESCE(a.duration_ms,0), a.meta_json, COALESCE(a.run_id,0),
		COALESCE(a.trace_id,''), COALESCE(a.span_id,''), COALESCE(a.parent_span_id,'')
		FROM app_logs a`)

	// Free-text: LIKE over message + meta_json. Each token must appear (AND),
	// case-insensitively. Cheap on the retention-capped table.
	if s := strings.TrimSpace(q.Q); s != "" {
		for _, tok := range strings.Fields(s) {
			where = append(where, `(a.message LIKE ? ESCAPE '\' OR a.meta_json LIKE ? ESCAPE '\')`)
			pat := "%" + likeEscape(tok) + "%"
			args = append(args, pat, pat)
		}
	}
	if q.Before > 0 {
		where = append(where, `a.id < ?`)
		args = append(args, q.Before)
	}
	if q.Category != "" {
		where = append(where, `a.category = ?`)
		args = append(args, q.Category)
	}
	if q.Project != "" {
		where = append(where, `a.project_id = ?`)
		args = append(args, q.Project)
	}
	if q.Repo != "" {
		where = append(where, `a.repo_id = ?`)
		args = append(args, q.Repo)
	}
	if q.Level != "" {
		where = append(where, `a.level = ?`)
		args = append(args, q.Level)
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	sb.WriteString(" ORDER BY a.id DESC LIMIT ?")
	args = append(args, limit+1)

	rows, err := l.db.Query(sb.String(), args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()

	out := make([]Record, 0, limit+1)
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.TS, &r.Level, &r.Category, &r.Event, &r.Message,
			&r.RepoID, &r.ProjectID, &r.Status, &r.DurationMs, &r.Meta, &r.RunID,
			&r.TraceID, &r.SpanID, &r.ParentSpanID); err != nil {
			return Page{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}

	page := Page{Logs: out}
	if len(out) > limit {
		page.Logs = out[:limit]
		page.NextCursor = page.Logs[len(page.Logs)-1].ID
	}
	return page, nil
}

// Categories returns the distinct categories present, for populating a filter.
func (l *Logger) Categories() ([]string, error) {
	return l.distinct("category")
}

// Projects returns the distinct non-empty project ids present.
func (l *Logger) Projects() ([]string, error) {
	return l.distinct("project_id")
}

func (l *Logger) distinct(col string) ([]string, error) {
	if l == nil || l.db == nil {
		return nil, nil
	}
	rows, err := l.db.Query(`SELECT DISTINCT ` + col + ` FROM app_logs WHERE ` + col + ` IS NOT NULL AND ` + col + ` <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s.Valid {
			out = append(out, s.String)
		}
	}
	return out, rows.Err()
}

// likeEscape escapes LIKE wildcards in a user token so they match literally
// (with ESCAPE '\' on the query).
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
