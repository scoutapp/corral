package convstore

import (
	"database/sql"
	"strings"
)

// Conversation is one conversation row as read back for the API/UI.
type Conversation struct {
	ID                   int64   `json:"id"`
	UUID                 string  `json:"uuid,omitempty"`
	ConvKey              string  `json:"convKey,omitempty"`
	ClaudeSessionID      string  `json:"claudeSessionId,omitempty"`
	CreatedAt            string  `json:"createdAt"`
	UpdatedAt            string  `json:"updatedAt"`
	OriginKind           string  `json:"originKind"`
	OriginID             string  `json:"originId,omitempty"`
	ProjectID            string  `json:"projectId,omitempty"`
	ProjectLabel         string  `json:"projectLabel,omitempty"`
	RepoID               string  `json:"repoId,omitempty"`
	PRNumber             int     `json:"prNumber,omitempty"`
	TraceID              string  `json:"traceId,omitempty"`
	ParentConversationID int64   `json:"parentConversationId,omitempty"`
	Title                string  `json:"title,omitempty"`
	FirstPrompt          string  `json:"firstPrompt,omitempty"`
	Model                string  `json:"model,omitempty"`
	TotalCostUSD         float64 `json:"totalCostUsd,omitempty"`
	Status               string  `json:"status"`
	MessageCount         int     `json:"messageCount"`
}

// MessageRow is one conv_messages row as read back.
type MessageRow struct {
	ID         int64  `json:"id"`
	Seq        int    `json:"seq"`
	TS         string `json:"ts"`
	Role       string `json:"role"`
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	ToolInput  string `json:"toolInput,omitempty"`
	ToolResult string `json:"toolResult,omitempty"`
	CostUSD    string `json:"costUsd,omitempty"`
	Model      string `json:"model,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// ListQuery filters a page of conversations. Empty fields are ignored. Before is
// the keyset cursor (id < Before; 0 = newest page). Q is an FTS query matched
// against message text (conversations whose messages match are returned).
type ListQuery struct {
	Before  int64
	Limit   int
	Origin  string
	Project string
	Repo    string
	Trace   string
	Parent  int64
	Q       string
}

// ListPage is a keyset page of conversations, newest-first.
type ListPage struct {
	Conversations []Conversation `json:"conversations"`
	NextCursor    int64          `json:"nextCursor"`
}

// List returns a keyset page of conversations matching the query. When Q is set,
// it restricts to conversations that have at least one message matching the FTS
// query (deep search across all captured text).
func (s *ConvStore) List(q ListQuery) (ListPage, error) {
	if s == nil || s.db == nil {
		return ListPage{Conversations: []Conversation{}}, nil
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
	sb.WriteString(convSelectCols + " FROM conversations c")

	// FTS deep-search: restrict to conversations with a matching message.
	if s := strings.TrimSpace(q.Q); s != "" {
		where = append(where, `c.id IN (
			SELECT m.conversation_id FROM conv_messages m
			JOIN conv_messages_fts f ON f.rowid = m.id
			WHERE conv_messages_fts MATCH ?)`)
		args = append(args, ftsQuery(s))
	}
	if q.Before > 0 {
		where = append(where, `c.id < ?`)
		args = append(args, q.Before)
	}
	if q.Origin != "" {
		where = append(where, `c.origin_kind = ?`)
		args = append(args, q.Origin)
	}
	if q.Project != "" {
		where = append(where, `c.project_id = ?`)
		args = append(args, q.Project)
	}
	if q.Repo != "" {
		where = append(where, `c.repo_id = ?`)
		args = append(args, q.Repo)
	}
	if q.Trace != "" {
		where = append(where, `c.trace_id = ?`)
		args = append(args, q.Trace)
	}
	if q.Parent > 0 {
		where = append(where, `c.parent_conversation_id = ?`)
		args = append(args, q.Parent)
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	sb.WriteString(" ORDER BY c.id DESC LIMIT ?")
	args = append(args, limit+1)

	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return ListPage{}, err
	}
	defer rows.Close()

	out := make([]Conversation, 0, limit+1)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return ListPage{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, err
	}

	page := ListPage{Conversations: out}
	if len(out) > limit {
		page.Conversations = out[:limit]
		page.NextCursor = page.Conversations[len(page.Conversations)-1].ID
	}
	return page, nil
}

// Get returns one conversation by id (or sql.ErrNoRows).
func (s *ConvStore) Get(id int64) (Conversation, error) {
	row := s.db.QueryRow(convSelectCols+" FROM conversations c WHERE c.id = ?", id)
	return scanConversation(row)
}

// UUID returns a conversation's stable public UUID, or "" if unknown. Cheap
// lookup used by the capture seam to surface the id to the chat UI.
func (s *ConvStore) UUID(id int64) string {
	if s == nil || s.db == nil || id == 0 {
		return ""
	}
	var u string
	_ = s.db.QueryRow(`SELECT COALESCE(uuid,'') FROM conversations WHERE id = ?`, id).Scan(&u)
	return u
}

// Messages returns a conversation's messages in order. When q is set, only
// messages matching the FTS query are returned (in-conversation search).
func (s *ConvStore) Messages(convID int64, q string) ([]MessageRow, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT m.id, m.seq, m.ts, m.role, m.type,
		COALESCE(m.text,''), COALESCE(m.tool_name,''), COALESCE(m.tool_input,''),
		COALESCE(m.tool_result,''), COALESCE(m.cost_usd,''), COALESCE(m.model,''), m.is_error
		FROM conv_messages m`)
	args = append(args, convID)
	if s := strings.TrimSpace(q); s != "" {
		sb.WriteString(` JOIN conv_messages_fts f ON f.rowid = m.id
			WHERE m.conversation_id = ? AND conv_messages_fts MATCH ?`)
		args = append(args, ftsQuery(s))
	} else {
		sb.WriteString(` WHERE m.conversation_id = ?`)
	}
	sb.WriteString(` ORDER BY m.seq ASC`)

	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageRow{}
	for rows.Next() {
		var m MessageRow
		if err := rows.Scan(&m.ID, &m.Seq, &m.TS, &m.Role, &m.Type, &m.Text,
			&m.ToolName, &m.ToolInput, &m.ToolResult, &m.CostUSD, &m.Model, &m.IsError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Chain returns the causal forest around a conversation: it walks
// parent_conversation_id up to the root, then collects all descendants of that
// root (BFS), so the caller gets every conversation in the same spawned chain —
// e.g. a global chat, the project it created, and that project's conversations.
// Ordered by id (creation order). A conversation with no links returns just
// itself. Bounded to avoid pathological cycles.
func (s *ConvStore) Chain(convID int64) ([]Conversation, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	// Walk up to the root via parent_conversation_id (cap the depth defensively).
	rootID := convID
	seenUp := map[int64]bool{}
	for i := 0; i < 100; i++ {
		if seenUp[rootID] {
			break // cycle guard
		}
		seenUp[rootID] = true
		var parent int64
		err := s.db.QueryRow(`SELECT COALESCE(parent_conversation_id,0) FROM conversations WHERE id = ?`, rootID).Scan(&parent)
		if err != nil || parent == 0 {
			break
		}
		rootID = parent
	}

	// BFS all descendants of the root.
	ids := []int64{rootID}
	seen := map[int64]bool{rootID: true}
	for i := 0; i < len(ids); i++ {
		rows, err := s.db.Query(`SELECT id FROM conversations WHERE parent_conversation_id = ? ORDER BY id`, ids[i])
		if err != nil {
			return nil, err
		}
		var children []int64
		for rows.Next() {
			var cid int64
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[cid] {
				seen[cid] = true
				children = append(children, cid)
			}
		}
		rows.Close()
		ids = append(ids, children...)
		if len(ids) > 1000 {
			break // safety cap
		}
	}

	// Fetch each conversation's metadata, ordered by id.
	out := make([]Conversation, 0, len(ids))
	for _, id := range ids {
		c, err := s.Get(id)
		if err != nil {
			continue // a member vanished (pruned) — skip it
		}
		out = append(out, c)
	}
	// Sort by id ascending for a stable, creation-ordered forest.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Origins returns the distinct origin_kind values present (for filter menus).
func (s *ConvStore) Origins() ([]string, error) { return s.distinct("origin_kind") }

// Projects returns the distinct non-empty project ids present.
func (s *ConvStore) Projects() ([]string, error) { return s.distinct("project_id") }

func (s *ConvStore) distinct(col string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT ` + col + ` FROM conversations WHERE ` + col + ` IS NOT NULL AND ` + col + ` <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.String)
		}
	}
	return out, rows.Err()
}

// --- shared column list + scanners ---

const convSelectCols = `SELECT c.id, COALESCE(c.uuid,''), c.conv_key, COALESCE(c.claude_session_id,''),
	c.created_at, c.updated_at, c.origin_kind, COALESCE(c.origin_id,''),
	COALESCE(c.project_id,''), COALESCE(c.project_label,''), COALESCE(c.repo_id,''),
	COALESCE(c.pr_number,0), COALESCE(c.trace_id,''), COALESCE(c.parent_conversation_id,0),
	COALESCE(c.title,''), COALESCE(c.first_prompt,''), COALESCE(c.model,''),
	c.total_cost_usd, c.status, c.message_count`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(...any) error }

func scanConversation(sc scanner) (Conversation, error) {
	var c Conversation
	err := sc.Scan(&c.ID, &c.UUID, &c.ConvKey, &c.ClaudeSessionID, &c.CreatedAt, &c.UpdatedAt,
		&c.OriginKind, &c.OriginID, &c.ProjectID, &c.ProjectLabel, &c.RepoID,
		&c.PRNumber, &c.TraceID, &c.ParentConversationID, &c.Title, &c.FirstPrompt,
		&c.Model, &c.TotalCostUSD, &c.Status, &c.MessageCount)
	return c, err
}

// ftsQuery turns a raw user query into a safe FTS5 MATCH expression: each token
// is quoted (so punctuation/operators are literal) and ANDed. Empty → matches
// nothing meaningful, but callers guard against empty before calling.
func ftsQuery(s string) string {
	var quoted []string
	for _, tok := range strings.Fields(s) {
		// Double any embedded quotes, then wrap — FTS5 string literals use "".
		quoted = append(quoted, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}
