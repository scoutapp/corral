package prreview

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// diffHunk is one contiguous change region within a file.
type diffHunk struct {
	filePath  string
	lineStart int
	lineEnd   int
	content   string
	isTest    bool
}

var testFileRes = []*regexp.Regexp{
	regexp.MustCompile(`\.test\.(ts|tsx|js|jsx)$`),
	regexp.MustCompile(`\.spec\.(ts|tsx|js|jsx)$`),
	regexp.MustCompile(`_test\.py$`),
	regexp.MustCompile(`(^|/)test_[^/]*\.py$`),
	regexp.MustCompile(`(^|/)(spec|tests|__tests__)/`),
	regexp.MustCompile(`(^|/)test/`),
	regexp.MustCompile(`_test\.go$`),
}

func isTestFile(path string) bool {
	p := strings.ToLower(path)
	for _, re := range testFileRes {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

var hunkHeaderRe = regexp.MustCompile(`\+(\d+)(?:,(\d+))?`)

// parseDiffHunks parses a unified diff into hunks, tracking each hunk's added
// line range (+c,d in the @@ header). Ported from the reference parse_diff_hunks.
func parseDiffHunks(rawDiff string) []diffHunk {
	var hunks []diffHunk
	var curFile string
	var curLines []string
	var curStart, curEnd int

	flush := func() {
		if curFile != "" && len(curLines) > 0 {
			hunks = append(hunks, diffHunk{
				filePath:  curFile,
				lineStart: curStart,
				lineEnd:   curEnd,
				content:   strings.Join(curLines, "\n"),
				isTest:    isTestFile(curFile),
			})
		}
		curLines = nil
	}

	for _, line := range strings.Split(rawDiff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			flush()
			curFile = line[6:]
			curStart, curEnd = 0, 0
		case strings.HasPrefix(line, "@@") && curFile != "":
			flush()
			if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
				curStart, _ = strconv.Atoi(m[1])
				length := 1
				if m[2] != "" {
					length, _ = strconv.Atoi(m[2])
				}
				curEnd = curStart + length - 1
			}
			curLines = []string{line}
		case curFile != "" && curLines != nil:
			curLines = append(curLines, line)
		}
	}
	flush()

	out := hunks[:0]
	for _, h := range hunks {
		if h.filePath != "" && h.filePath != "/dev/null" {
			out = append(out, h)
		}
	}
	return out
}

// groupHunks merges hunks in the same file within 10 lines of each other into
// one logical block. Ported from group_hunks_into_blocks.
func groupHunks(hunks []diffHunk) [][]diffHunk {
	if len(hunks) == 0 {
		return nil
	}
	groups := [][]diffHunk{{hunks[0]}}
	for _, h := range hunks[1:] {
		g := &groups[len(groups)-1]
		prev := (*g)[len(*g)-1]
		if h.filePath == prev.filePath && h.lineStart-prev.lineEnd <= 10 {
			*g = append(*g, h)
		} else {
			groups = append(groups, []diffHunk{h})
		}
	}
	return groups
}

var commentPrefixes = []string{"#", "//", "*", "/*", "*/", `"""`, "'''", "--", "%", ";"}

// isCommentOnlyDiff reports whether every changed (+/-) line is blank or a
// comment. Such blocks are forced to trivial importance.
func isCommentOnlyDiff(diff string) bool {
	var changed []string
	for _, line := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
			(strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
			changed = append(changed, strings.TrimSpace(line[1:]))
		}
	}
	if len(changed) == 0 {
		return false
	}
	for _, l := range changed {
		if l == "" {
			continue
		}
		isComment := false
		for _, p := range commentPrefixes {
			if strings.HasPrefix(l, p) {
				isComment = true
				break
			}
		}
		if !isComment {
			return false
		}
	}
	return true
}

// ExtractBlocks parses a PR's stored raw_diff into hotness-ranked blocks with
// Claude-generated analysis, replacing any existing blocks for the PR. ai may
// be nil (placeholders used). Also generates and stores the PR's <=100-char
// short summary. Returns the blocks in presentation order.
func (s *Service) ExtractBlocks(ctx context.Context, prID int64, ai aiRunner) ([]Block, error) {
	// Load the PR (repo_id, title, raw_diff).
	var repoID, title, rawDiff string
	var number int
	err := s.db.QueryRow(`
		SELECT repo_id, COALESCE(title,''), COALESCE(raw_diff,''), pr_number
		  FROM prs WHERE id = ?`, prID).Scan(&repoID, &title, &rawDiff, &number)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rawDiff) == "" {
		return []Block{}, nil
	}

	// File churn baseline for hotness (default 1.0 when unanalyzed).
	churn := map[string]float64{}
	if rows, err := s.db.Query(
		`SELECT file_path, COALESCE(churn_score, 1.0) FROM pr_file_stats WHERE repo_id = ?`, repoID,
	); err == nil {
		for rows.Next() {
			var p string
			var c float64
			if rows.Scan(&p, &c) == nil {
				churn[p] = c
			}
		}
		rows.Close()
	}

	// Callgraph in-degree per file (max in-degree of any node in the file), when
	// a callgraph has been built. Empty map ⇒ hotness stays churn-only.
	indeg, _ := s.InDegrees(repoID)

	groups := groupHunks(parseDiffHunks(rawDiff))

	var items []builtBlock

	for _, group := range groups {
		combined := make([]string, 0, len(group))
		filePath := group[0].filePath
		lineStart, lineEnd := group[0].lineStart, group[0].lineEnd
		for _, h := range group {
			combined = append(combined, h.content)
			if h.lineStart < lineStart {
				lineStart = h.lineStart
			}
			if h.lineEnd > lineEnd {
				lineEnd = h.lineEnd
			}
		}
		diff := strings.Join(combined, "\n")

		a := analyzeBlock(ctx, ai, diff, filePath, title)
		importance := a.Importance
		switch {
		case isCommentOnlyDiff(diff):
			importance = 5
		case group[0].isTest:
			if importance < 4 {
				importance = 4
			}
		case importance == 5:
			importance = 3 // Claude called real code trivial → moderate
		}
		c := churn[filePath]
		if c == 0 {
			c = 1.0
		}
		// Hotness blends churn, AI importance, and callgraph in-degree: a change
		// in a heavily-called, high-churn, important file is hottest. The
		// (1 + in_degree) factor is 1 when no callgraph exists, so this degrades
		// cleanly to the churn-only formula from Phase 2.
		hotness := c * float64(6-importance) * float64(1+indeg[filePath])

		items = append(items, builtBlock{
			b: Block{
				PRID:            prID,
				FilePath:        filePath,
				LineStart:       lineStart,
				LineEnd:         lineEnd,
				DiffHunk:        diff,
				Title:           a.Title,
				Explanation:     a.Explanation,
				CodebaseContext: a.CodebaseContext,
				HotnessScore:    &hotness,
				IsTest:          group[0].isTest,
			},
			edgeCases: a.EdgeCases,
		})
	}

	// Rank by hotness DESC; order_index/priority follow the ranking.
	sort.SliceStable(items, func(i, j int) bool {
		return *items[i].b.HotnessScore > *items[j].b.HotnessScore
	})

	// Stamp the repo-analysis these blocks were ranked against, so staleness can
	// be detected later. Empty when the repo isn't analyzed (⇒ blocks unranked).
	analysisSHA := s.repoAnalysisSHA(repoID)
	if err := s.writeBlocks(prID, items, title, number, analysisSHA, ctx, ai); err != nil {
		return nil, err
	}
	return s.Blocks(prID)
}

// repoAnalysisSHA returns the head_sha the repo was last analyzed at, or "".
func (s *Service) repoAnalysisSHA(repoID string) string {
	var sha string
	_ = s.db.QueryRow(
		`SELECT head_sha FROM pr_repo_analysis WHERE repo_id = ?`, repoID,
	).Scan(&sha)
	return sha
}

// BlocksStatus reports whether a PR's block hotness ranking is current relative
// to the repo's analysis.
type BlocksStatus struct {
	// RepoAnalyzed is false when the repo has never been analyzed (blocks are
	// unranked — all fallback hotness).
	RepoAnalyzed bool `json:"repoAnalyzed"`
	// Stale is true when the blocks were ranked against a different repo-analysis
	// than the current one (repo analyzed/re-analyzed after the blocks were
	// extracted) — hotness ranking is out of date.
	Stale bool `json:"stale"`
}

// BlocksStatusFor returns the block-ranking freshness for a PR: whether the repo
// is analyzed and whether the blocks were ranked against the current analysis.
func (s *Service) BlocksStatusFor(prID int64) (BlocksStatus, error) {
	var repoID, blocksSHA string
	var blocksSHANull *string
	if err := s.db.QueryRow(
		`SELECT repo_id, blocks_analysis_sha FROM prs WHERE id = ?`, prID,
	).Scan(&repoID, &blocksSHANull); err != nil {
		return BlocksStatus{}, err
	}
	if blocksSHANull != nil {
		blocksSHA = *blocksSHANull
	}
	current := s.repoAnalysisSHA(repoID)

	st := BlocksStatus{RepoAnalyzed: current != ""}
	// Stale when the repo is analyzed but the blocks were ranked against a
	// different (older/absent) analysis sha.
	if st.RepoAnalyzed && blocksSHA != current {
		st.Stale = true
	}
	return st, nil
}

// builtBlock is an extracted block plus its edge cases, before persistence.
type builtBlock struct {
	b         Block
	edgeCases []edgeCase
}

// writeBlocks persists blocks + edge cases for a PR (replacing prior rows) and
// updates the PR's short summary, atomically.
func (s *Service) writeBlocks(prID int64, items []builtBlock, title string, number int, analysisSHA string, ctx context.Context, ai aiRunner) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM pr_blocks WHERE pr_id = ?`, prID); err != nil {
		return err
	}

	blockTitles := make([]string, 0, len(items))
	for i, it := range items {
		res, err := tx.Exec(`
			INSERT INTO pr_blocks
			    (pr_id, order_index, priority, file_path, line_start, line_end,
			     diff_hunk, title, explanation, codebase_context, hotness_score, is_test)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			prID, i, i+1, it.b.FilePath, it.b.LineStart, it.b.LineEnd,
			it.b.DiffHunk, it.b.Title, it.b.Explanation, it.b.CodebaseContext,
			it.b.HotnessScore, boolToInt(it.b.IsTest),
		)
		if err != nil {
			return err
		}
		blockID, _ := res.LastInsertId()
		for _, ec := range it.edgeCases {
			sev := ec.Severity
			if sev == "" {
				sev = "low"
			}
			if _, err := tx.Exec(
				`INSERT INTO pr_block_edge_cases (block_id, description, severity) VALUES (?, ?, ?)`,
				blockID, ec.Description, sev,
			); err != nil {
				return err
			}
		}
		if it.b.Title != "" {
			blockTitles = append(blockTitles, it.b.Title)
		}
	}

	// PR short summary from the (now ranked) block titles.
	fallback := title
	if fallback == "" {
		fallback = "PR #" + strconv.Itoa(number)
	}
	limit := blockTitles
	if len(limit) > 5 {
		limit = limit[:5]
	}
	summary := summarizePR(ctx, ai, title, limit, fallback)
	if _, err := tx.Exec(`
		UPDATE prs
		   SET short_summary = ?, blocks_analysis_sha = ?, blocks_extracted_at = datetime('now')
		 WHERE id = ?`, summary, analysisSHA, prID); err != nil {
		return err
	}

	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
