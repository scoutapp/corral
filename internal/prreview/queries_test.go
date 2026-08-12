package prreview

import (
	"testing"

	"github.com/scoutapp/corral/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	s, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s), s
}

func TestForensicsEmpty(t *testing.T) {
	svc, _ := newService(t)
	got, err := svc.Forensics("repo-abc")
	if err != nil {
		t.Fatalf("Forensics: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d rows", len(got))
	}
}

func TestForensicsOrdersByChurnNullsLast(t *testing.T) {
	svc, s := newService(t)
	db := s.DB()
	// Two scored files + one unscored; expect churn DESC then nulls last.
	if _, err := db.Exec(`
		INSERT INTO pr_file_stats (repo_id, file_path, total_commits, fix_commits, churn_score)
		VALUES ('r', 'low.go', 3, 1, 0.5),
		       ('r', 'high.go', 9, 4, 4.2),
		       ('r', 'nul.go', 1, 0, NULL)
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := svc.Forensics("r")
	if err != nil {
		t.Fatalf("Forensics: %v", err)
	}
	want := []string{"high.go", "low.go", "nul.go"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].FilePath != w {
			t.Errorf("row %d = %s, want %s", i, got[i].FilePath, w)
		}
	}
}

func TestPRsAndBlocks(t *testing.T) {
	svc, s := newService(t)
	db := s.DB()
	res, err := db.Exec(`
		INSERT INTO prs (repo_id, pr_number, title, state, fetched_at)
		VALUES ('r', 247, 'Idempotency keys', 'open', datetime('now'))
	`)
	if err != nil {
		t.Fatalf("insert pr: %v", err)
	}
	prID, _ := res.LastInsertId()

	prs, err := svc.PRs("r")
	if err != nil {
		t.Fatalf("PRs: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 247 || prs[0].Title != "Idempotency keys" {
		t.Fatalf("unexpected PRs: %+v", prs)
	}

	if _, err := db.Exec(`
		INSERT INTO pr_blocks (pr_id, order_index, file_path, line_start, line_end, is_test)
		VALUES (?, 0, 'src/charge.ts', 47, 89, 0)
	`, prID); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	blocks, err := svc.Blocks(prID)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].FilePath != "src/charge.ts" || blocks[0].LineStart != 47 {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
}
