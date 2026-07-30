package dashboard

import (
	"os/exec"
	"testing"
)

// initGitRepo makes a throwaway repo with two commits on main and a branch that
// adds a file, so ref-diffing has something real to compare.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := exec.Command("sh", "-c", "echo hello > "+dir+"/a.txt").Run(); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first")
	run("checkout", "-q", "-b", "feature")
	if err := exec.Command("sh", "-c", "echo world > "+dir+"/b.txt").Run(); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-q", "-m", "add b")
	run("checkout", "-q", "main")
	return dir
}

func TestValidRef(t *testing.T) {
	dir := initGitRepo(t)
	cases := []struct {
		ref  string
		want bool
	}{
		{"main", true},
		{"feature", true},
		{"HEAD", true},
		{"", false},
		{"no-such-branch", false},
		{"$(touch HACKED)", false}, // injection attempt — must not validate
		{"main; rm -rf /", false},
	}
	for _, c := range cases {
		if got := validRef(dir, c.ref); got != c.want {
			t.Errorf("validRef(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

func TestGitShow(t *testing.T) {
	dir := initGitRepo(t)
	// a.txt exists on both refs; b.txt only on feature (added there).
	if got := gitShow(dir, "main", "a.txt"); got == "" {
		t.Error("gitShow(main, a.txt) empty, expected content")
	}
	if got := gitShow(dir, "main", "b.txt"); got != "" {
		t.Errorf("gitShow(main, b.txt) = %q, want empty (absent on main)", got)
	}
	if got := gitShow(dir, "feature", "b.txt"); got == "" {
		t.Error("gitShow(feature, b.txt) empty, expected content")
	}
	if got := gitShow(dir, "no-such-ref", "a.txt"); got != "" {
		t.Errorf("gitShow(bad ref) = %q, want empty", got)
	}
}

func TestGitRefList(t *testing.T) {
	dir := initGitRepo(t)
	branches := gitRefList(dir, "refs/heads")
	has := map[string]bool{}
	for _, b := range branches {
		has[b] = true
	}
	if !has["main"] || !has["feature"] {
		t.Errorf("gitRefList missing branches, got %v", branches)
	}
}
