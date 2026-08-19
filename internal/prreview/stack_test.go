package prreview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStackDetection builds a repo with a stack (main → A → B) and verifies
// Stack() reports the direct/transitive ancestry, ignoring an unrelated PR.
func TestStackDetection(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", name)
		git("commit", "-q", "-m", name)
		return git("rev-parse", "HEAD")
	}

	git("init", "-q", "-b", "main")
	baseSHA := commit("base") // main tip
	// Branch A off main.
	git("checkout", "-q", "-b", "feat-a")
	aSHA := commit("a")
	// Branch B off A (stacked directly on A).
	git("checkout", "-q", "-b", "feat-b")
	bSHA := commit("b")
	// An unrelated branch off main (not in A/B's history).
	git("checkout", "-q", "main")
	git("checkout", "-q", "-b", "feat-x")
	xSHA := commit("x")

	gitDir := filepath.Join(dir, ".git")

	svc, _ := newService(t)
	mkPR := func(num int, head, base string) int64 {
		res, err := svc.db.Exec(
			`INSERT INTO prs (repo_id, pr_number, title, head_sha, base_sha, fetched_at)
			 VALUES (?,?,?,?,?,datetime('now'))`, "r1", num, "PR", head, base)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	prA := mkPR(1, aSHA, baseSHA) // A: base = main tip
	prB := mkPR(2, bSHA, aSHA)    // B: base = A's head  → direct stack on A
	mkPR(3, xSHA, baseSHA)        // X: unrelated

	// From B: stacked ON A (direct — B.base == A.head). Nothing depends on B.
	res, err := svc.Stack(prB, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StackedOn) != 1 || res.StackedOn[0].PRID != prA {
		t.Fatalf("B.stackedOn = %+v, want [A]", res.StackedOn)
	}
	if res.StackedOn[0].Relation != "direct" {
		t.Errorf("B→A should be direct, got %q", res.StackedOn[0].Relation)
	}
	if len(res.Dependents) != 0 {
		t.Errorf("B should have no dependents, got %+v", res.Dependents)
	}

	// From A: nothing above it (its base is main, no PR has main's tip as head),
	// and B is a dependent (direct). X is unrelated and must NOT appear.
	res, err = svc.Stack(prA, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StackedOn) != 0 {
		t.Errorf("A.stackedOn should be empty, got %+v", res.StackedOn)
	}
	if len(res.Dependents) != 1 || res.Dependents[0].PRID != prB {
		t.Fatalf("A.dependents = %+v, want [B]", res.Dependents)
	}
	if res.Dependents[0].Relation != "direct" {
		t.Errorf("B→A dependent should be direct, got %q", res.Dependents[0].Relation)
	}
}

// TestIsAncestorGuards covers the trivial short-circuits.
func TestIsAncestorGuards(t *testing.T) {
	if isAncestor("", "a", "b") {
		t.Error("empty gitDir should be false")
	}
	if isAncestor("/nonexistent.git", "a", "a") {
		t.Error("same sha should be false (not its own ancestor here)")
	}
}
