package prreview

import (
	"strings"
)

// PRLink is a directed relationship from one PR to another.
type PRLink struct {
	ID           int64  `json:"id"`
	PRID         int64  `json:"prId"`
	LinkedPRID   int64  `json:"linkedPrId"`
	Relationship string `json:"relationship"` // tests | tested_by | related | depends_on
	Note         string `json:"note,omitempty"`
	// Denormalized target fields for display (filled by Links).
	LinkedNumber  int    `json:"linkedNumber,omitempty"`
	LinkedTitle   string `json:"linkedTitle,omitempty"`
	LinkedSummary string `json:"linkedSummary,omitempty"`
}

// LinkSuggestion is a candidate PR to link, ranked by changed-file overlap.
type LinkSuggestion struct {
	PRID    int64  `json:"prId"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Overlap int    `json:"overlap"` // count of shared changed files
}

// diffFiles returns the distinct files a diff touches (the +++ b/ paths),
// reusing the block diff parser so parsing stays in one place.
func diffFiles(rawDiff string) map[string]bool {
	files := map[string]bool{}
	for _, h := range parseDiffHunks(rawDiff) {
		files[h.filePath] = true
	}
	return files
}

// SuggestLinks returns other PRs in the same repo whose changed files overlap
// the target PR's, most-overlap first (up to limit). Ported from the reference
// suggest_linked_prs.
func (s *Service) SuggestLinks(prID int64, limit int) ([]LinkSuggestion, error) {
	var repoID, rawDiff string
	if err := s.db.QueryRow(
		`SELECT repo_id, COALESCE(raw_diff,'') FROM prs WHERE id = ?`, prID,
	).Scan(&repoID, &rawDiff); err != nil {
		return nil, err
	}
	target := diffFiles(rawDiff)
	if len(target) == 0 {
		return []LinkSuggestion{}, nil
	}

	rows, err := s.db.Query(`
		SELECT id, pr_number, COALESCE(title,''), COALESCE(raw_diff,'')
		  FROM prs WHERE repo_id = ? AND id != ?`, repoID, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Exclude PRs already linked from the target.
	linked, err := s.linkedIDs(prID)
	if err != nil {
		return nil, err
	}

	var out []LinkSuggestion
	for rows.Next() {
		var id int64
		var number int
		var title, diff string
		if err := rows.Scan(&id, &number, &title, &diff); err != nil {
			return nil, err
		}
		if linked[id] || diff == "" {
			continue
		}
		overlap := 0
		for f := range diffFiles(diff) {
			if target[f] {
				overlap++
			}
		}
		if overlap > 0 {
			out = append(out, LinkSuggestion{PRID: id, Number: number, Title: title, Overlap: overlap})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Most overlap first; stable by number for ties.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func less(a, b LinkSuggestion) bool {
	if a.Overlap != b.Overlap {
		return a.Overlap > b.Overlap
	}
	return a.Number < b.Number
}

// Links returns the relationships originating from a PR, with the target PR's
// number/title/summary denormalized for display.
func (s *Service) Links(prID int64) ([]PRLink, error) {
	rows, err := s.db.Query(`
		SELECT l.id, l.pr_id, l.linked_pr_id, COALESCE(l.relationship,''),
		       COALESCE(l.note,''), p.pr_number, COALESCE(p.title,''),
		       COALESCE(p.short_summary,'')
		  FROM pr_links l
		  JOIN prs p ON p.id = l.linked_pr_id
		 WHERE l.pr_id = ?
		 ORDER BY l.id`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PRLink{}
	for rows.Next() {
		var l PRLink
		if err := rows.Scan(&l.ID, &l.PRID, &l.LinkedPRID, &l.Relationship,
			&l.Note, &l.LinkedNumber, &l.LinkedTitle, &l.LinkedSummary); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AddLink creates a relationship prID -> linkedPRID. relationship is validated
// against the known set; note is free text. Returns the created link.
func (s *Service) AddLink(prID, linkedPRID int64, relationship, note string) (*PRLink, error) {
	rel := strings.TrimSpace(relationship)
	switch rel {
	case "tests", "tested_by", "related", "depends_on":
	default:
		rel = "related"
	}
	res, err := s.db.Exec(`
		INSERT INTO pr_links (pr_id, linked_pr_id, relationship, note)
		VALUES (?, ?, ?, ?)`, prID, linkedPRID, rel, note)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &PRLink{ID: id, PRID: prID, LinkedPRID: linkedPRID, Relationship: rel, Note: note}, nil
}

// RemoveLink deletes a link by id.
func (s *Service) RemoveLink(linkID int64) error {
	_, err := s.db.Exec(`DELETE FROM pr_links WHERE id = ?`, linkID)
	return err
}

// linkedIDs returns the set of PR ids already linked from prID.
func (s *Service) linkedIDs(prID int64) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT linked_pr_id FROM pr_links WHERE pr_id = ?`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}
