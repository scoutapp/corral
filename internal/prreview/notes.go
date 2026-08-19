package prreview

import (
	"fmt"
	"strings"
)

// PRNote is a private, local annotation on a PR. Unlike a PR comment (which is
// posted to GitHub via Comment), a note lives only in Corral's DB and is never
// sent upstream — a scratchpad for you/your team while reviewing.
type PRNote struct {
	ID        int64  `json:"id"`
	PRID      int64  `json:"prId"`
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Notes returns a PR's local notes, newest first.
func (s *Service) Notes(prID int64) ([]PRNote, error) {
	rows, err := s.db.Query(`
		SELECT id, pr_id, body, COALESCE(author,''), created_at
		  FROM pr_notes
		 WHERE pr_id = ?
		 ORDER BY id DESC`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PRNote{}
	for rows.Next() {
		var n PRNote
		if err := rows.Scan(&n.ID, &n.PRID, &n.Body, &n.Author, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNote saves a local note on a PR. body is required; author is an optional
// free-text label (e.g. "cli"). Returns the created note.
func (s *Service) AddNote(prID int64, body, author string) (*PRNote, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("note body is required")
	}
	res, err := s.db.Exec(
		`INSERT INTO pr_notes (pr_id, body, author) VALUES (?, ?, ?)`,
		prID, body, strings.TrimSpace(author))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	// Read back the row so created_at (DB default) is returned to the caller.
	var n PRNote
	err = s.db.QueryRow(
		`SELECT id, pr_id, body, COALESCE(author,''), created_at FROM pr_notes WHERE id = ?`, id,
	).Scan(&n.ID, &n.PRID, &n.Body, &n.Author, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// RemoveNote deletes a note by id. Returns an error if the note doesn't exist,
// so a bad id is a clear 404 rather than a silent no-op.
func (s *Service) RemoveNote(noteID int64) error {
	res, err := s.db.Exec(`DELETE FROM pr_notes WHERE id = ?`, noteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note %d not found", noteID)
	}
	return nil
}
