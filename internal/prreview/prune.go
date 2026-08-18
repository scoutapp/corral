package prreview

import (
	"fmt"
	"time"
)

// Prune deletes LOCAL PR records (the prs table) whose fetched_at — when Corral
// last cached the PR from GitHub — is older than olderThanDays. This is a
// local-only cache cleanup: it NEVER touches GitHub (no closing/deleting PRs
// upstream). Dependent rows (pr_blocks, pr_links, pr_chat_messages) are removed
// automatically via ON DELETE CASCADE. repoID, when non-empty, scopes the prune
// to one repo. Returns how many PR rows were deleted.
//
// olderThanDays must be >= 1 so a prune can't wipe just-cached PRs by accident.
func (s *Service) Prune(olderThanDays int, repoID string) (int, error) {
	if olderThanDays < 1 {
		return 0, fmt.Errorf("olderThanDays must be >= 1")
	}
	cutoff := pruneCutoff(olderThanDays)
	q := `DELETE FROM prs WHERE fetched_at < ?`
	args := []any{cutoff}
	if repoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, repoID)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("prune prs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PruneCount returns how many PR records WOULD be pruned (the dry-run for Prune),
// without deleting anything. Same cutoff + repo semantics.
func (s *Service) PruneCount(olderThanDays int, repoID string) (int, error) {
	if olderThanDays < 1 {
		return 0, fmt.Errorf("olderThanDays must be >= 1")
	}
	cutoff := pruneCutoff(olderThanDays)
	q := `SELECT COUNT(*) FROM prs WHERE fetched_at < ?`
	args := []any{cutoff}
	if repoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, repoID)
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count prunable prs: %w", err)
	}
	return n, nil
}

// pruneCutoff returns the RFC3339-UTC cutoff timestamp: records with fetched_at
// lexically less than this are older than olderThanDays. fetched_at is stored as
// RFC3339 UTC (see gh.go upsertPR), which sorts lexicographically, so a string
// comparison is a correct chronological comparison.
func pruneCutoff(olderThanDays int) string {
	return time.Now().UTC().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Format(time.RFC3339)
}
