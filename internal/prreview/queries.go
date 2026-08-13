package prreview

import (
	"database/sql"
	"strings"
	"time"

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
		       churn_score, author_count, first_commit, last_commit, last_analyzed
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
			&f.ChurnScore, &f.AuthorCount, &f.FirstCommit, &f.LastCommit, &f.LastAnalyzed,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FileForensics returns the rich per-file forensics for the files a PR touches,
// hottest first. Combines stored git stats with derived staleness/velocity and
// a callgraph reference count (distinct OTHER files that call into this file).
// Files without stored stats (repo not analyzed) still appear with zeros.
func (s *Service) FileForensics(prID int64, nowTS int64) ([]FileForensic, error) {
	// The distinct files this PR's blocks touch.
	var repoID string
	if err := s.db.QueryRow(`SELECT repo_id FROM prs WHERE id = ?`, prID).Scan(&repoID); err != nil {
		return nil, err
	}
	// Files the PR touches, HOTTEST FIRST: order by each file's max block
	// hotness (a file's hottest change region), then by path for stability. This
	// puts the highest-signal files at the top of the "Files changed" panel.
	fileRows, err := s.db.Query(`
		SELECT file_path, MAX(COALESCE(hotness_score, 0)) AS hot
		  FROM pr_blocks
		 WHERE pr_id = ?
		 GROUP BY file_path
		 ORDER BY hot DESC, file_path ASC`, prID)
	if err != nil {
		return nil, err
	}
	var files []string
	for fileRows.Next() {
		var p string
		var hot float64
		if err := fileRows.Scan(&p, &hot); err != nil {
			fileRows.Close()
			return nil, err
		}
		files = append(files, p)
	}
	fileRows.Close()

	// Callgraph ref counts: for each file, how many distinct OTHER files contain
	// a caller of a node defined in this file.
	refCounts, _ := s.refCounts(repoID)

	// Per-file +/- from this PR's diff (summed over the file's block hunks).
	addDel := s.diffStatsByFile(prID)

	// Has the repo been analyzed at all? Distinguishes a new file (analyzed repo,
	// no row for this path ⇒ PR adds it) from "repo never analyzed".
	repoAnalyzed := s.repoAnalyzed(repoID)

	out := []FileForensic{}
	for _, fp := range files {
		var total, fix, authors int
		var churn *float64
		var first, last *int64
		err := s.db.QueryRow(`
			SELECT total_commits, fix_commits, author_count, churn_score,
			       first_commit, last_commit
			  FROM pr_file_stats WHERE repo_id = ? AND file_path = ?`,
			repoID, fp,
		).Scan(&total, &fix, &authors, &churn, &first, &last)
		if err != nil {
			// No stats row. If the repo is analyzed, this file has no history on
			// the analyzed branch — i.e. the PR adds it (NewFile). Otherwise the
			// repo simply hasn't been analyzed yet.
			out = append(out, FileForensic{
				FilePath: fp, RefCount: refCounts[fp],
				Additions: addDel[fp][0], Deletions: addDel[fp][1],
				NewFile: repoAnalyzed, RepoAnalyzed: repoAnalyzed,
			})
			continue
		}
		f := FileForensic{
			FilePath: fp, TotalCommits: total, FixCommits: fix,
			AuthorCount: authors, ChurnScore: churn, RefCount: refCounts[fp],
			Additions: addDel[fp][0], Deletions: addDel[fp][1],
			RepoAnalyzed: repoAnalyzed,
		}
		if total > 0 {
			f.FixPct = int(float64(fix) / float64(total) * 100)
		}
		if first != nil {
			d := int((nowTS - *first) / 86400)
			f.DaysOld = &d
			if d > 0 {
				f.VelocityPerWeek = round2(float64(total) / (float64(d) / 7))
			}
		}
		if last != nil {
			d := int((nowTS - *last) / 86400)
			f.DaysSinceEdit = &d
		}
		out = append(out, f)
	}
	return out, nil
}

// repoAnalyzed reports whether the repo has a recorded analysis. Prefers the
// pr_repo_analysis marker; falls back to "any pr_file_stats rows exist" so a
// forensics run recorded before the marker migration still counts.
func (s *Service) repoAnalyzed(repoID string) bool {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pr_repo_analysis WHERE repo_id = ?`, repoID,
	).Scan(&n); err == nil && n > 0 {
		return true
	}
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM pr_file_stats WHERE repo_id = ? LIMIT 1`, repoID,
	).Scan(&n)
	return n > 0
}

// diffStatsByFile sums +/- lines per file across a PR's block diff hunks.
// Returns map file_path → [additions, deletions]. Counts changed lines only
// (skips +++/--- headers and @@ hunk headers).
func (s *Service) diffStatsByFile(prID int64) map[string][2]int {
	out := map[string][2]int{}
	rows, err := s.db.Query(
		`SELECT file_path, COALESCE(diff_hunk,'') FROM pr_blocks WHERE pr_id = ?`, prID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var fp, hunk string
		if rows.Scan(&fp, &hunk) != nil {
			continue
		}
		add, del := out[fp][0], out[fp][1]
		for _, line := range strings.Split(hunk, "\n") {
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				add++
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				del++
			}
		}
		out[fp] = [2]int{add, del}
	}
	return out
}

// fileHeuristics loads the per-file signals (churn, fix/total commits, authors,
// staleness) for a repo and folds in the callgraph in-degree, for the block AI
// prompt + the mechanical hotness fallback. Files with no stats row are absent
// from the map (callers default churn to 1.0).
func (s *Service) fileHeuristics(repoID string, indeg map[string]int) map[string]fileHeuristic {
	out := map[string]fileHeuristic{}
	now := time.Now().Unix()
	rows, err := s.db.Query(`
		SELECT file_path, COALESCE(churn_score,1.0), total_commits, fix_commits,
		       author_count, last_commit
		  FROM pr_file_stats WHERE repo_id = ?`, repoID)
	if err == nil {
		for rows.Next() {
			var fp string
			var churn float64
			var total, fix, authors int
			var last *int64
			if rows.Scan(&fp, &churn, &total, &fix, &authors, &last) != nil {
				continue
			}
			h := fileHeuristic{
				Churn: churn, TotalCommits: total, FixCommits: fix,
				AuthorCount: authors, InDegree: indeg[fp], DaysSinceEdit: -1,
			}
			if last != nil {
				h.DaysSinceEdit = int((now - *last) / 86400)
			}
			out[fp] = h
		}
		rows.Close()
	}
	// Files that have an in-degree but no stats row still get the in-degree.
	for fp, d := range indeg {
		if _, ok := out[fp]; !ok {
			out[fp] = fileHeuristic{Churn: 1.0, InDegree: d, DaysSinceEdit: -1}
		}
	}
	return out
}

// refCounts maps file_path → number of distinct other files that call into it
// (callgraph in-references from elsewhere). Empty when no callgraph exists.
func (s *Service) refCounts(repoID string) (map[string]int, error) {
	rows, err := s.db.Query(`
		SELECT callee.file_path, COUNT(DISTINCT caller.file_path)
		  FROM pr_cg_edges e
		  JOIN pr_cg_nodes callee ON callee.id = e.callee_id
		  JOIN pr_cg_nodes caller ON caller.id = e.caller_id
		 WHERE e.repo_id = ? AND caller.file_path != callee.file_path
		 GROUP BY callee.file_path`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var f string
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			return nil, err
		}
		out[f] = n
	}
	return out, rows.Err()
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// PRs returns the pull requests fetched for a repo, newest fetch first.
func (s *Service) PRs(repoID string) ([]PR, error) {
	rows, err := s.db.Query(`
		SELECT id, repo_id, pr_number, COALESCE(title, ''), COALESCE(body, ''),
		       COALESCE(short_summary, ''), COALESCE(github_url, ''),
		       COALESCE(state, ''), COALESCE(base_sha, ''),
		       COALESCE(head_sha, ''), COALESCE(head_ref, ''), fetched_at
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
			&p.ID, &p.RepoID, &p.Number, &p.Title, &p.Body, &p.ShortSummary,
			&p.GithubURL, &p.State, &p.BaseSHA, &p.HeadSHA, &p.HeadRef, &p.FetchedAt,
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
