package convstore

import "fmt"

// Retention bounds conversations.db so it never grows without limit. Two caps,
// applied together, ON CONVERSATIONS (messages + fts rows cascade via FK/triggers
// when a conversation is deleted):
//   - by age:   delete conversations older than MaxAgeDays (by created_at)
//   - by count: keep only the newest MaxRows conversations (by id)
//
// A zero/negative bound disables that cap. Defaults are larger than the app-log
// defaults — conversations are the point of this DB.
type Retention struct {
	MaxAgeDays int
	MaxRows    int
}

// DefaultRetention is the zero-config policy: 30 days, 500k conversations.
var DefaultRetention = Retention{MaxAgeDays: 30, MaxRows: 500_000}

// Prune enforces the policy. Best-effort: returns the number of CONVERSATIONS
// deleted and any error. Deleting a conversation cascades to its messages (FK
// ON DELETE CASCADE) and their FTS rows (AFTER DELETE trigger).
func (s *ConvStore) Prune(r Retention) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var total int64

	if r.MaxAgeDays > 0 {
		cutoff := fmt.Sprintf("-%d days", r.MaxAgeDays)
		res, err := s.db.Exec(
			`DELETE FROM conversations WHERE created_at < strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	if r.MaxRows > 0 {
		res, err := s.db.Exec(`
			DELETE FROM conversations
			 WHERE id < (
			   SELECT MIN(id) FROM (SELECT id FROM conversations ORDER BY id DESC LIMIT ?)
			 )`, r.MaxRows)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	if total > 0 {
		_, _ = s.db.Exec(`PRAGMA incremental_vacuum`)
	}
	return total, nil
}

// Count returns the current number of conversations (for tests / diagnostics).
func (s *ConvStore) Count() (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&n)
	return n, err
}
