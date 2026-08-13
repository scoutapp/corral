package prreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAnalysisStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("a.go", "package p\nfunc a(){}\n")
	git("add", "-A")
	git("commit", "-qm", "first")
	gitDir := filepath.Join(dir, ".git")

	svc, _ := newService(t)

	// Before analysis: not analyzed.
	st, err := svc.AnalysisStatusFor("r1", gitDir, "")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Analyzed {
		t.Error("expected not-analyzed before Analyze")
	}

	// Analyze → records HEAD; up to date.
	if _, err := svc.AnalyzeRepo(context.Background(), "r1", gitDir, ""); err != nil {
		t.Fatalf("AnalyzeRepo: %v", err)
	}
	st, _ = svc.AnalysisStatusFor("r1", gitDir, "")
	if !st.Analyzed || !st.UpToDate {
		t.Fatalf("expected analyzed + up-to-date, got %+v", st)
	}
	if len(st.NewCommits) != 0 {
		t.Errorf("expected no new commits, got %d", len(st.NewCommits))
	}

	// Add two commits → stale, both listed newest-first.
	write("a.go", "package p\nfunc a(){}\nfunc b(){}\n")
	git("add", "-A")
	git("commit", "-qm", "add b")
	write("a.go", "package p\nfunc a(){}\nfunc b(){}\nfunc c(){}\n")
	git("add", "-A")
	git("commit", "-qm", "add c")

	st, _ = svc.AnalysisStatusFor("r1", gitDir, "")
	if st.UpToDate {
		t.Fatal("expected stale after new commits")
	}
	if len(st.NewCommits) != 2 {
		t.Fatalf("expected 2 new commits, got %d: %+v", len(st.NewCommits), st.NewCommits)
	}
	if st.NewCommits[0].Subject != "add c" || st.NewCommits[1].Subject != "add b" {
		t.Errorf("new commits not newest-first: %+v", st.NewCommits)
	}

	// Re-analyze → up to date again.
	if _, err := svc.AnalyzeRepo(context.Background(), "r1", gitDir, ""); err != nil {
		t.Fatalf("re-analyze: %v", err)
	}
	st, _ = svc.AnalysisStatusFor("r1", gitDir, "")
	if !st.UpToDate {
		t.Errorf("expected up-to-date after re-analyze, got %+v", st)
	}
}
