package store

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// migrationFiles holds the ordered, append-only schema migrations. Each file is
// named NNNN_description.sql (e.g. 0001_prreview.sql) and is applied exactly
// once, in filename order. NEVER edit a migration that has shipped — add a new
// numbered file instead. This is what lets future Corral features add or alter
// tables in the shared DB without a rewrite.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate applies every migration not yet recorded in schema_migrations, in
// order, each in its own transaction. Applied versions are tracked by filename.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.applyMigration(name, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// applyMigration runs one migration and records it, atomically.
func (s *Store) applyMigration(name, body string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version) VALUES (?)`, name,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) appliedVersions() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrationNames returns the *.sql migration filenames in lexical (== numeric)
// order.
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
