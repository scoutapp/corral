package prreview

import "testing"

func TestPrune(t *testing.T) {
	svc, _ := newService(t)

	// An OLD PR (fetched 40 days ago) with a dependent block + link, and a RECENT
	// PR (fetched now). Prune older-than-30 should remove only the old one, and
	// cascade to its block.
	oldID := insertPRAt(t, svc, "repo-1", 100, "-40 days")
	recentID := insertPRAt(t, svc, "repo-1", 101, "-1 days")
	svc.db.Exec(`INSERT INTO pr_blocks (pr_id, order_index, priority, file_path, line_start, line_end, is_test)
	             VALUES (?, 0, 0, 'a.go', 1, 2, 0)`, oldID)

	// Dry run first: 1 would be pruned.
	if n, err := svc.PruneCount(30, ""); err != nil || n != 1 {
		t.Fatalf("PruneCount(30) = %d, %v; want 1", n, err)
	}

	// Prune.
	n, err := svc.Prune(30, "")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}

	// Old PR gone, recent PR kept.
	if prExists(t, svc, oldID) {
		t.Error("old PR should have been pruned")
	}
	if !prExists(t, svc, recentID) {
		t.Error("recent PR should have been kept")
	}
	// Its block cascaded away.
	var blocks int
	svc.db.QueryRow(`SELECT COUNT(*) FROM pr_blocks WHERE pr_id = ?`, oldID).Scan(&blocks)
	if blocks != 0 {
		t.Errorf("old PR's blocks should cascade-delete, got %d", blocks)
	}

	// Floor: olderThanDays < 1 is rejected.
	if _, err := svc.Prune(0, ""); err == nil {
		t.Error("Prune(0) should error (floor >= 1)")
	}
	if _, err := svc.PruneCount(0, ""); err == nil {
		t.Error("PruneCount(0) should error (floor >= 1)")
	}
}

func TestPruneScopedToRepo(t *testing.T) {
	svc, _ := newService(t)
	insertPRAt(t, svc, "repo-A", 1, "-40 days")
	insertPRAt(t, svc, "repo-B", 1, "-40 days")

	// Prune only repo-A.
	n, err := svc.Prune(30, "repo-A")
	if err != nil || n != 1 {
		t.Fatalf("Prune(repo-A) = %d, %v; want 1", n, err)
	}
	// repo-B's old PR survives.
	if got, _ := svc.PruneCount(30, "repo-B"); got != 1 {
		t.Errorf("repo-B should still have 1 prunable PR, got %d", got)
	}
}

// insertPRAt inserts a PR whose fetched_at is `offset` from now (e.g. "-40 days")
// using SQLite's datetime(), formatted to match the stored RFC3339-ish shape.
func insertPRAt(t *testing.T, svc *Service, repoID string, number int, offset string) int64 {
	t.Helper()
	// strftime with the RFC3339 'T'/'Z' so it lexically compares with cutoffs.
	res, err := svc.db.Exec(`
		INSERT INTO prs (repo_id, pr_number, title, fetched_at)
		VALUES (?, ?, 'x', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?))`, repoID, number, offset)
	if err != nil {
		t.Fatalf("insert pr: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func prExists(t *testing.T, svc *Service, id int64) bool {
	t.Helper()
	var n int
	svc.db.QueryRow(`SELECT COUNT(*) FROM prs WHERE id = ?`, id).Scan(&n)
	return n > 0
}
