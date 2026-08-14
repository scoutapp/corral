package applog

import "fmt"

// Retention bounds the log so corral.db never grows without limit. Two caps,
// applied together:
//   - by age:   delete rows older than MaxAgeDays
//   - by count: keep only the newest MaxRows
// Deleting is cheap; we run an incremental vacuum afterward so freed pages
// return to the OS.
type Retention struct {
	MaxAgeDays int
	MaxRows    int
}

// DefaultRetention is the zero-config policy: 30 days, 100k rows.
var DefaultRetention = Retention{MaxAgeDays: 30, MaxRows: 100_000}

// Prune enforces the policy. Best-effort: returns the number of rows deleted and
// any error, but callers typically ignore the error (a prune hiccup shouldn't
// disrupt anything). A zero/negative bound disables that cap.
func (l *Logger) Prune(r Retention) (int64, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	var total int64

	if r.MaxAgeDays > 0 {
		cutoff := fmt.Sprintf("-%d days", r.MaxAgeDays)
		res, err := l.db.Exec(
			`DELETE FROM app_logs WHERE ts < strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	if r.MaxRows > 0 {
		// Delete everything older than the newest MaxRows rows (by id).
		res, err := l.db.Exec(`
			DELETE FROM app_logs
			 WHERE id < (
			   SELECT MIN(id) FROM (SELECT id FROM app_logs ORDER BY id DESC LIMIT ?)
			 )`, r.MaxRows)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	if total > 0 {
		// Best-effort reclaim (no-op unless auto_vacuum=incremental; harmless).
		_, _ = l.db.Exec(`PRAGMA incremental_vacuum`)
	}
	return total, nil
}

// Count returns the current number of log rows (for tests / diagnostics).
func (l *Logger) Count() (int64, error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	var n int64
	err := l.db.QueryRow(`SELECT COUNT(*) FROM app_logs`).Scan(&n)
	return n, err
}
