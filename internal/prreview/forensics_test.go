package prreview

import (
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
