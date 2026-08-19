// Package convstore owns Corral's SECOND embedded SQLite database
// (~/.corral/conversations.db) — a dedicated, write-heavy store for captured
// Claude conversations, kept separate from the main app DB (corral.db) so its
// high insert volume and retention churn never contend with the interactive app
// state. Every Claude conversation in the app — global chat, project chat,
// PR-review chat, merge/worker jobs, one-shot analyses, the draft flows, and the
// sandbox's own Claude — is captured here as a conversation with its messages
// (including tool calls), linked to its origin and into the distributed trace.
//
// It deliberately mirrors internal/store (WAL DSN, embedded migrations) but is a
// distinct connection pool and file. Full-text search uses SQLite FTS5, so
// corral is built with the `sqlite_fts5` tag (see install.sh + .goreleaser.*).
package convstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scoutapp/corral/internal/config"

	_ "github.com/mattn/go-sqlite3"
)

// ConvStore wraps the single *sql.DB handle to conversations.db. Safe for
// concurrent use (database/sql pools connections).
type ConvStore struct {
	db *sql.DB
}

// DBPath returns the on-disk location of the conversations database
// (~/.corral/conversations.db, honoring $CORRAL_HOME).
func DBPath() string {
	return filepath.Join(config.CorralHome(), "conversations.db")
}

// Open opens (creating if needed) the conversations database and applies any
// outstanding migrations. Idempotent.
func Open() (*ConvStore, error) {
	return openAt(DBPath())
}

// openAt opens the database at an explicit path. Exposed for tests.
func openAt(path string) (*ConvStore, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("convstore: create data dir: %w", err)
		}
	}

	// Same DSN posture as the main store: wait under contention, enforce FKs,
	// WAL for read/write concurrency.
	dsn := path + "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("convstore: open %s: %w", path, err)
	}

	// go-sqlite3 serializes writes on a single connection; a small pool avoids
	// "database is locked" while still allowing concurrent reads.
	db.SetMaxOpenConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("convstore: ping %s: %w", path, err)
	}

	s := &ConvStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("convstore: migrate: %w", err)
	}
	return s, nil
}

// DB returns the underlying *sql.DB.
func (s *ConvStore) DB() *sql.DB { return s.db }

// Close closes the database handle.
func (s *ConvStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
