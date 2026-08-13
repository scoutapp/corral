package prreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo builds a throwaway git repo and returns its .git dir. Commits
// are made with fixed author/committer dates so churn is deterministic.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append([]string{
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		}, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(nil, "init", "-q")
	// commit dates span ~10 days so age is well over the 1-day floor.
	d := func(day int) []string {
		ts := "2026-01-0" + string(rune('1'+day)) + "T12:00:00"
		return []string{"GIT_AUTHOR_DATE=" + ts, "GIT_COMMITTER_DATE=" + ts}
	}
	// hot.go: 3 commits, 2 of them fixes.
	write("hot.go", "1")
	run(d(0), "add", "-A")
	run(d(0), "commit", "-qm", "add hot")
	write("hot.go", "2")
	run(d(2), "add", "-A")
	run(d(2), "commit", "-qm", "fix crash in hot")
	write("hot.go", "3")
	run(d(4), "add", "-A")
	run(d(4), "commit", "-qm", "bugfix: hot edge case")
	// cold.go: 1 non-fix commit. "fixture" must NOT count as a fix.
	write("cold.go", "1")
	run(d(6), "add", "-A")
	run(d(6), "commit", "-qm", "add cold fixture data")

	return filepath.Join(dir, ".git")
}

func TestAnalyze(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitDir := initTestRepo(t)
	svc, _ := newService(t)

	stats, err := svc.Analyze("r1", gitDir)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	by := map[string]FileStat{}
	for _, s := range stats {
		by[s.FilePath] = s
	}
	if len(by) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(by), stats)
	}

	hot := by["hot.go"]
	if hot.TotalCommits != 3 {
		t.Errorf("hot.go total = %d, want 3", hot.TotalCommits)
	}
	if hot.FixCommits != 2 {
		t.Errorf("hot.go fix = %d, want 2 (fix + bugfix)", hot.FixCommits)
	}

	cold := by["cold.go"]
	if cold.TotalCommits != 1 {
		t.Errorf("cold.go total = %d, want 1", cold.TotalCommits)
	}
	if cold.FixCommits != 0 {
		t.Errorf("cold.go fix = %d, want 0 ('fixture' is not a fix)", cold.FixCommits)
	}

	// Forensics() sorts hottest first; hot.go (more, more-recent commits) leads.
	if stats[0].FilePath != "hot.go" {
		t.Errorf("hottest = %s, want hot.go", stats[0].FilePath)
	}

	// Re-analyzing replaces, not appends.
	stats2, err := svc.Analyze("r1", gitDir)
	if err != nil {
		t.Fatalf("re-Analyze: %v", err)
	}
	if len(stats2) != 2 {
		t.Errorf("after re-analyze got %d rows, want 2 (replace, not append)", len(stats2))
	}
}

func TestChurnScoreFloor(t *testing.T) {
	// age < 1 day clamps to 1 day.
	if got := churnScore(5, 1000, 1000); got != 5.0 {
		t.Errorf("churn with zero age = %v, want 5.0 (1-day floor)", got)
	}
	// 10 commits over 10 days = 1.0/day.
	if got := churnScore(10, 0, 10*86400); got != 1.0 {
		t.Errorf("churn = %v, want 1.0", got)
	}
}

func TestAnalyzeCapturesAuthors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitDir := initTestRepo(t) // all commits by t@e (single author)
	svc, _ := newService(t)
	if _, err := svc.Analyze("r1", gitDir); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var authors int
	var first, last *int64
	svc.db.QueryRow(`SELECT author_count, first_commit, last_commit
	                 FROM pr_file_stats WHERE repo_id='r1' AND file_path='hot.go'`).
		Scan(&authors, &first, &last)
	if authors != 1 {
		t.Errorf("author_count = %d, want 1 (sole contributor)", authors)
	}
	if first == nil || last == nil || *last < *first {
		t.Errorf("expected first<=last commit ts, got first=%v last=%v", first, last)
	}
}

func TestFileForensics(t *testing.T) {
	svc, _ := newService(t)
	now := int64(1_700_000_000)
	// charge.ts: 100 commits, 40 fixes, 1 author, first 400d ago, last 5d ago.
	first := now - 400*86400
	last := now - 5*86400
	svc.db.Exec(`INSERT INTO pr_file_stats
	  (repo_id, file_path, total_commits, fix_commits, churn_score, author_count, first_commit, last_commit)
	  VALUES ('r1','src/charge.ts',100,40,5.0,1,?,?)`, first, last)
	// callgraph: charge.ts referenced by 2 other files.
	svc.db.Exec(`INSERT INTO pr_cg_nodes (id,repo_id,file_path,symbol_name,kind,line_start,line_end)
	  VALUES (1,'r1','src/charge.ts','charge','function',1,9),
	         (2,'r1','src/a.ts','a','function',1,3),(3,'r1','src/b.ts','b','function',1,3)`)
	svc.db.Exec(`INSERT INTO pr_cg_edges (repo_id,caller_id,callee_id) VALUES ('r1',2,1),('r1',3,1)`)

	prID := seedPR(t, svc, "r1", sampleDiff)
	svc.ExtractBlocks(context.Background(), prID, nil) // creates the block for charge.ts

	stats, err := svc.FileForensics(prID, now)
	if err != nil {
		t.Fatalf("FileForensics: %v", err)
	}
	var charge *FileForensic
	for i := range stats {
		if stats[i].FilePath == "src/charge.ts" {
			charge = &stats[i]
		}
	}
	if charge == nil {
		t.Fatal("no forensics for src/charge.ts")
	}
	if charge.FixPct != 40 {
		t.Errorf("fixPct = %d, want 40", charge.FixPct)
	}
	if charge.AuthorCount != 1 {
		t.Errorf("authorCount = %d, want 1", charge.AuthorCount)
	}
	if charge.DaysOld == nil || *charge.DaysOld != 400 {
		t.Errorf("daysOld = %v, want 400", charge.DaysOld)
	}
	if charge.DaysSinceEdit == nil || *charge.DaysSinceEdit != 5 {
		t.Errorf("daysSinceEdit = %v, want 5", charge.DaysSinceEdit)
	}
	if charge.RefCount != 2 {
		t.Errorf("refCount = %d, want 2 (a.ts, b.ts call charge)", charge.RefCount)
	}
	if charge.VelocityPerWeek <= 0 {
		t.Errorf("velocityPerWeek = %v, want > 0", charge.VelocityPerWeek)
	}
}

func TestFileForensicsNewVsUnanalyzed(t *testing.T) {
	svc, _ := newService(t)
	now := int64(1_700_000_000)

	// Repo r-analyzed: has an analysis marker + one file with history. The PR
	// touches that file AND a brand-new file.
	svc.db.Exec(`INSERT INTO pr_repo_analysis (repo_id, head_sha) VALUES ('r-an','abc')`)
	svc.db.Exec(`INSERT INTO pr_file_stats
	  (repo_id, file_path, total_commits, fix_commits, churn_score, author_count, first_commit, last_commit)
	  VALUES ('r-an','app/models/org.rb',306,30,0.5,24,?,?)`, now-4050*86400, now-9*86400)

	diff := "+++ b/app/models/org.rb\n@@ -1 +1 @@\n+x\n" +
		"+++ b/app/jobs/new_job.rb\n@@ -0,0 +1 @@\n+brand new\n"
	prID := seedPRWithDiff(t, svc, "r-an", 5, diff)
	svc.ExtractBlocks(context.Background(), prID, nil)

	stats, err := svc.FileForensics(prID, now)
	if err != nil {
		t.Fatalf("FileForensics: %v", err)
	}
	by := map[string]FileForensic{}
	for _, f := range stats {
		by[f.FilePath] = f
	}
	org := by["app/models/org.rb"]
	if !org.RepoAnalyzed || org.NewFile || org.TotalCommits != 306 {
		t.Errorf("org.rb should be analyzed existing file: %+v", org)
	}
	nw := by["app/jobs/new_job.rb"]
	if !nw.RepoAnalyzed {
		t.Errorf("new file's repoAnalyzed should be true (repo IS analyzed)")
	}
	if !nw.NewFile {
		t.Errorf("new_job.rb should be flagged NewFile (added by the PR), got %+v", nw)
	}

	// Repo r-none: never analyzed. A touched file is 'not analyzed', not 'new'.
	diff2 := "+++ b/lib/thing.rb\n@@ -1 +1 @@\n+x\n"
	prID2 := seedPRWithDiff(t, svc, "r-none", 6, diff2)
	svc.ExtractBlocks(context.Background(), prID2, nil)
	stats2, _ := svc.FileForensics(prID2, now)
	if len(stats2) != 1 {
		t.Fatalf("want 1 file, got %d", len(stats2))
	}
	if stats2[0].RepoAnalyzed {
		t.Errorf("r-none repoAnalyzed should be false")
	}
	if stats2[0].NewFile {
		t.Errorf("un-analyzed repo file should NOT be flagged NewFile")
	}
}
