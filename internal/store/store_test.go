package store

import (
	"path/filepath"
	"testing"
)

// openTemp opens a store in a throwaway directory.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := openAt(filepath.Join(t.TempDir(), "corral.db"))
	if err != nil {
		t.Fatalf("openAt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := openTemp(t)

	// Every migration should be recorded.
	names, err := migrationNames()
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected at least one migration file")
	}
	applied, err := s.appliedVersions()
	if err != nil {
		t.Fatalf("appliedVersions: %v", err)
	}
	for _, n := range names {
		if !applied[n] {
			t.Errorf("migration %s not recorded as applied", n)
		}
	}

	// The first migration's tables should exist and be queryable.
	for _, table := range []string{
		"pr_file_stats", "pr_cg_nodes", "pr_cg_edges", "prs",
		"pr_blocks", "pr_block_edge_cases", "pr_links", "pr_chat_messages",
	} {
		if _, err := s.DB().Exec("SELECT COUNT(*) FROM " + table); err != nil {
			t.Errorf("table %s not usable: %v", table, err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corral.db")

	s1, err := openAt(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()

	// Re-opening the same file must not re-run migrations or error.
	s2, err := openAt(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	var count int
	if err := s2.DB().QueryRow(
		`SELECT COUNT(*) FROM schema_migrations`,
	).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	names, _ := migrationNames()
	if count != len(names) {
		t.Errorf("expected %d applied migrations, got %d", len(names), count)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := openTemp(t)
	// Inserting a block that references a non-existent PR must fail with FKs on.
	_, err := s.DB().Exec(`
		INSERT INTO pr_blocks (pr_id, order_index, file_path, line_start, line_end)
		VALUES (99999, 0, 'x.go', 1, 2)
	`)
	if err == nil {
		t.Fatal("expected foreign-key violation, got nil")
	}
}
