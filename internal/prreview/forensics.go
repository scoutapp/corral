package prreview

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// fixPattern matches commit subjects that look like fixes. Whole-word so
// "fixture"/"prefix"/"suffix" don't count. Ported from the reference
// git_forensics.py FIX_PATTERN.
var fixPattern = regexp.MustCompile(`(?i)\b(fix|bug|hotfix|patch|bugfix)\b`)

// fileAgg accumulates per-file commit stats during a single git-log pass.
type fileAgg struct {
	total   int
	fix     int
	firstTS int64 // earliest commit unix ts touching the file
}

// Analyze computes per-file forensics for a repo and writes them to
// pr_file_stats (replacing any prior rows for the repo). gitDir is a path git
// can run in — corral's bare mirror (Repo.CachePath) works directly. Returns
// the resulting stats, hottest first.
//
// Unlike the reference (which ran `git log --follow` once per file), this makes
// a single `git log --name-only` pass over all commits and buckets per file —
// same data, far fewer subprocesses on large repos. --follow across renames is
// the one thing lost; churn ranking doesn't need it.
func (s *Service) Analyze(repoID, gitDir string) ([]FileStat, error) {
	aggs, err := gitLogAggregate(gitDir)
	if err != nil {
		return nil, fmt.Errorf("prreview: git log: %w", err)
	}

	now := time.Now().Unix()
	nowRFC := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM pr_file_stats WHERE repo_id = ?`, repoID); err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO pr_file_stats
		    (repo_id, file_path, total_commits, fix_commits, churn_score, last_analyzed)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for path, a := range aggs {
		churn := churnScore(a.total, a.firstTS, now)
		if _, err := stmt.Exec(repoID, path, a.total, a.fix, churn, nowRFC); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Forensics(repoID)
}

// AnalyzeResult reports what a full repo analysis produced.
type AnalyzeResult struct {
	Files       []FileStat `json:"files"`
	Nodes       int        `json:"cgNodes"`
	Edges       int        `json:"cgEdges"`
	CallgraphOK bool       `json:"callgraphOk"`
}

// AnalyzeRepo runs forensics AND the tree-sitter callgraph for a repo. The
// callgraph is best-effort: if it fails (e.g. `git archive`/parse trouble on a
// large or unusual repo) the forensics result is still returned, with
// CallgraphOK=false, so hotness gracefully falls back to churn-only.
func (s *Service) AnalyzeRepo(ctx context.Context, repoID, gitDir, defaultBranch string) (AnalyzeResult, error) {
	files, err := s.Analyze(repoID, gitDir)
	if err != nil {
		return AnalyzeResult{}, err
	}
	res := AnalyzeResult{Files: files}
	nodes, edges, cgErr := s.BuildCallgraph(ctx, repoID, gitDir, defaultBranch)
	if cgErr == nil {
		res.Nodes, res.Edges, res.CallgraphOK = nodes, edges, true
	}
	return res, nil
}

// churnScore = total_commits / age_in_days, with a 1-day floor to avoid
// division by zero (matches the reference).
func churnScore(total int, firstTS, nowTS int64) float64 {
	ageDays := float64(nowTS-firstTS) / 86400.0
	if ageDays < 1.0 {
		ageDays = 1.0
	}
	return float64(total) / ageDays
}

// gitLogAggregate walks every commit once, attributing each touched file a
// commit (and a fix-commit if the subject matches) and tracking the earliest
// timestamp seen per file.
//
// The git format emits, per commit: a \x01 sentinel, then "<unixts> <subject>",
// a newline, then the name-only file list. -z makes that file list
// NUL-delimited (so paths with spaces/newlines are safe). We split the whole
// output on \x01 into per-commit records and parse each.
func gitLogAggregate(gitDir string) (map[string]*fileAgg, error) {
	cmd := exec.Command("git",
		"--git-dir", gitDir,
		"log", "--no-merges", "-z",
		"--format=%x01%at %s", "--name-only",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	aggs := map[string]*fileAgg{}
	for _, rec := range strings.Split(string(out), "\x01") {
		if rec == "" {
			continue
		}
		// rec = "<ts> <subject>\n<NUL-separated file paths>"
		nl := strings.IndexByte(rec, '\n')
		if nl < 0 {
			continue // a commit that touched no files (shouldn't happen with --no-merges)
		}
		header, rest := rec[:nl], rec[nl+1:]

		sp := strings.IndexByte(header, ' ')
		if sp < 0 {
			continue
		}
		ts, err := strconv.ParseInt(header[:sp], 10, 64)
		if err != nil {
			continue
		}
		isFix := fixPattern.MatchString(header[sp+1:])

		for _, path := range strings.Split(rest, "\x00") {
			path = strings.Trim(path, "\n")
			if path == "" {
				continue
			}
			a := aggs[path]
			if a == nil {
				a = &fileAgg{firstTS: ts}
				aggs[path] = a
			}
			a.total++
			if isFix {
				a.fix++
			}
			if ts < a.firstTS {
				a.firstTS = ts
			}
		}
	}
	return aggs, nil
}
