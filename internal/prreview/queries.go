package prreview

import (
	"database/sql"

	"github.com/scoutapp/corral/internal/store"
)

// Service is the store-backed data layer for PR Review. It holds no state of
// its own beyond the shared *store.Store; construct one per request or reuse a
// single instance — both are safe (database/sql pools connections).
type Service struct {
	db *sql.DB
}

// New wraps the shared store.
func New(s *store.Store) *Service {
	return &Service{db: s.DB()}
}

// Forensics returns a repo's per-file stats, hottest first (highest churn).
// Files with no churn score sort last. Returns an empty slice when the repo has
// not been analyzed yet — the caller shows an "analyze" affordance.
func (s *Service) Forensics(repoID string) ([]FileStat, error) {
	rows, err := s.db.Query(`
		SELECT id, repo_id, file_path, total_commits, fix_commits,
		       churn_score, last_analyzed
		  FROM pr_file_stats
		 WHERE repo_id = ?
		 ORDER BY churn_score DESC NULLS LAST, fix_commits DESC
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FileStat{}
	for rows.Next() {
		var f FileStat
		if err := rows.Scan(
			&f.ID, &f.RepoID, &f.FilePath, &f.TotalCommits, &f.FixCommits,
			&f.ChurnScore, &f.LastAnalyzed,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PRs returns the pull requests fetched for a repo, newest fetch first.
func (s *Service) PRs(repoID string) ([]PR, error) {
	rows, err := s.db.Query(`
		SELECT id, repo_id, pr_number, COALESCE(title, ''),
		       COALESCE(short_summary, ''), COALESCE(github_url, ''),
		       COALESCE(state, ''), COALESCE(base_sha, ''),
		       COALESCE(head_sha, ''), fetched_at
		  FROM prs
		 WHERE repo_id = ?
		 ORDER BY fetched_at DESC, pr_number DESC
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PR{}
	for rows.Next() {
		var p PR
		if err := rows.Scan(
			&p.ID, &p.RepoID, &p.Number, &p.Title, &p.ShortSummary,
			&p.GithubURL, &p.State, &p.BaseSHA, &p.HeadSHA, &p.FetchedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Blocks returns a PR's blocks in presentation order (order_index ascending,
// which the extractor sets by descending hotness).
func (s *Service) Blocks(prID int64) ([]Block, error) {
	rows, err := s.db.Query(`
		SELECT id, pr_id, order_index, priority, file_path, line_start, line_end,
		       COALESCE(diff_hunk, ''), COALESCE(title, ''),
		       COALESCE(explanation, ''), COALESCE(codebase_context, ''),
		       hotness_score, is_test
		  FROM pr_blocks
		 WHERE pr_id = ?
		 ORDER BY order_index ASC
	`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Block{}
	for rows.Next() {
		var b Block
		if err := rows.Scan(
			&b.ID, &b.PRID, &b.OrderIndex, &b.Priority, &b.FilePath,
			&b.LineStart, &b.LineEnd, &b.DiffHunk, &b.Title, &b.Explanation,
			&b.CodebaseContext, &b.HotnessScore, &b.IsTest,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
