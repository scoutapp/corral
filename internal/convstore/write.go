package convstore

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
)

// newUUID returns a random RFC-4122 v4 UUID string. Uses crypto/rand (no
// external dependency — matches the repo's existing id generation). Panics only
// if the system RNG fails, which is unrecoverable anyway.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("convstore: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ConvMeta is the origin/linkage metadata for a conversation, set at start.
type ConvMeta struct {
	ConvKey              string // stable upsert key
	ClaudeSessionID      string
	OriginKind           string
	OriginID             string
	ProjectID            string
	ProjectLabel         string
	RepoID               string
	PRNumber             int
	TraceID              string
	RootSpanID           string
	ParentConversationID int64
	Title                string
	FirstPrompt          string
}

// Message is one captured turn frame. Fields mirror the dashboard's
// chatServerMsg so the capture tee maps 1:1.
type Message struct {
	Role       string
	Type       string
	Text       string
	ToolName   string
	ToolInput  string
	ToolResult string
	CostUSD    string
	Model      string
	IsError    bool
	MetaJSON   string
}

// StartConversation inserts (or upserts by conv_key) a conversation and returns
// its id. On conflict it updates the mutable identity fields (session id, title)
// so a placeholder key can be promoted once the real Claude session id arrives.
func (s *ConvStore) StartConversation(m ConvMeta) (int64, error) {
	if m.ConvKey == "" {
		m.ConvKey = m.OriginKind + ":" + m.OriginID
	}
	var parent any
	if m.ParentConversationID > 0 {
		parent = m.ParentConversationID
	}
	// uuid is assigned once, on genuine insert, and NEVER updated on conflict —
	// it's the conversation's stable public handle. (It's deliberately absent from
	// the DO UPDATE SET below.)
	res, err := s.db.Exec(`
		INSERT INTO conversations
		    (conv_key, uuid, claude_session_id, origin_kind, origin_id, project_id,
		     project_label, repo_id, pr_number, trace_id, root_span_id,
		     parent_conversation_id, title, first_prompt)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(conv_key) DO UPDATE SET
		    claude_session_id = COALESCE(NULLIF(excluded.claude_session_id,''), conversations.claude_session_id),
		    title             = COALESCE(NULLIF(excluded.title,''), conversations.title),
		    updated_at        = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, m.ConvKey, newUUID(), m.ClaudeSessionID, m.OriginKind, m.OriginID, nullif(m.ProjectID),
		nullif(m.ProjectLabel), nullif(m.RepoID), nullifInt(m.PRNumber), nullif(m.TraceID),
		nullif(m.RootSpanID), parent, nullif(m.Title), nullif(m.FirstPrompt))
	if err != nil {
		return 0, err
	}
	// LastInsertId is only valid for the INSERT path; on an upsert we must look up
	// the existing row by its unique key.
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		// Confirm this id actually belongs to our conv_key (an upsert can still
		// report a rowid); re-select to be safe.
		var got int64
		if s.db.QueryRow(`SELECT id FROM conversations WHERE conv_key = ?`, m.ConvKey).Scan(&got) == nil {
			return got, nil
		}
		return id, nil
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM conversations WHERE conv_key = ?`, m.ConvKey).Scan(&id)
	return id, err
}

// SetSessionID records the Claude session id once the stream reveals it (and can
// promote the conv_key to the session-based form). Best-effort.
func (s *ConvStore) SetSessionID(convID int64, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	_, err := s.db.Exec(
		`UPDATE conversations SET claude_session_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		sessionID, convID)
	return err
}

// AppendMessage inserts a message at the next seq and rolls up conversation
// summary fields (count, updated_at, model, cost, first_prompt/title).
func (s *ConvStore) AppendMessage(convID int64, m Message) error {
	if m.MetaJSON == "" {
		m.MetaJSON = "{}"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var seq int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(seq)+1, 0) FROM conv_messages WHERE conversation_id = ?`, convID,
	).Scan(&seq); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO conv_messages
		    (conversation_id, seq, role, type, text, tool_name, tool_input,
		     tool_result, cost_usd, model, is_error, meta_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		convID, seq, m.Role, m.Type, nullif(m.Text), nullif(m.ToolName), nullif(m.ToolInput),
		nullif(m.ToolResult), nullif(m.CostUSD), nullif(m.Model), boolInt(m.IsError), m.MetaJSON,
	); err != nil {
		return err
	}

	// Roll up the conversation summary. first_prompt/title default from the first
	// user text; model/cost track the latest result frame.
	if _, err := tx.Exec(`
		UPDATE conversations SET
		    message_count = message_count + 1,
		    updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		    model         = COALESCE(NULLIF(?,''), model),
		    total_cost_usd = total_cost_usd + ?,
		    first_prompt  = COALESCE(first_prompt, ?),
		    title         = COALESCE(title, ?)
		WHERE id = ?`,
		m.Model, parseCost(m.CostUSD),
		firstPromptFrom(m), firstPromptFrom(m), convID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// SetStatus stamps a terminal (or any) status on a conversation.
func (s *ConvStore) SetStatus(convID int64, status string) error {
	_, err := s.db.Exec(
		`UPDATE conversations SET status = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		status, convID)
	return err
}

// MarkRunningInterrupted downgrades any still-"running" conversation to
// "interrupted" on startup — their process didn't survive a restart. Mirrors the
// merge-job reconciliation pattern.
func (s *ConvStore) MarkRunningInterrupted() error {
	_, err := s.db.Exec(`UPDATE conversations SET status = 'interrupted' WHERE status = 'running'`)
	return err
}

// --- small helpers ---

// firstPromptFrom returns the user text of a message (for first_prompt/title
// defaulting), else "" so COALESCE leaves the existing value.
func firstPromptFrom(m Message) any {
	if m.Role == "user" && strings.TrimSpace(m.Text) != "" {
		t := m.Text
		if len(t) > 200 {
			t = t[:200]
		}
		return t
	}
	return nil
}

func nullif(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullifInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parseCost turns a formatted cost string ("$0.0123" / "0.0123") into a float for
// summing; returns 0 on anything unparseable (cost is a best-effort rollup).
func parseCost(s string) float64 {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
