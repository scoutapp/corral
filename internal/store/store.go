// Package store owns Corral's single embedded SQLite database
// (~/.corral/corral.db). It is Corral-wide infrastructure: features register
// their tables as numbered migrations under migrations/ and share the one
// connection pool. PR Review is the first tenant; more of Corral's state may
// move in over time.
//
// The database is intentionally NOT a home for existing JSON state
// (repos.json, projects.json, …) — those stay as-is for now. Only new state
// (starting with PR Review) lives here.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scoutapp/corral/internal/config"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps the single *sql.DB handle to corral.db. It is safe for
// concurrent use by multiple goroutines (database/sql pools connections).
type Store struct {
	db *sql.DB
}

// DBPath returns the on-disk location of the Corral database
// (~/.corral/corral.db, honoring $CORRAL_HOME).
func DBPath() string {
	return filepath.Join(config.CorralHome(), "corral.db")
}

// Open opens (creating if needed) the Corral database and applies any
// outstanding migrations. It is idempotent: calling it on an up-to-date
// database is a no-op beyond opening the handle.
func Open() (*Store, error) {
	return openAt(DBPath())
}

// openAt opens the database at an explicit path. Exposed for tests, which
// point it at a temp file; production code uses Open.
func openAt(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}

	// _busy_timeout: wait rather than immediately erroring under contention.
	// _foreign_keys: enforce FK constraints (off by default in SQLite).
	// _journal_mode=WAL: better read/write concurrency for the dashboard.
	dsn := path + "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// go-sqlite3 serializes writes on a single connection; a small pool avoids
	// "database is locked" surprises while still allowing concurrent reads.
	db.SetMaxOpenConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// DB returns the underlying *sql.DB so feature packages (e.g. internal/prreview)
// can run their own queries against the shared connection pool.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
