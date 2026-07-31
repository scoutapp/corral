package repos

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeOrigin creates a real local git repo with one commit, to serve as a
// clone/fetch source (no network — Add/Fetch clone from this path).
func makeOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@e.com")
	git("config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	git("add", ".")
	git("commit", "-q", "-m", "init")
	return dir
}

func TestReposAddFetchRemove(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", t.TempDir())
	origin := makeOrigin(t)

	// Add → registered + a bare mirror cache created.
	repo, err := Add(AddOptions{URL: origin})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if repo.CachePath == "" {
		t.Fatal("no cache path set")
	}
	if fi, err := os.Stat(repo.CachePath); err != nil || !fi.IsDir() {
		t.Fatalf("cache mirror not created at %s: %v", repo.CachePath, err)
	}
	// A --mirror clone is bare: it has no working tree (no a.txt on disk there).
	if _, err := os.Stat(filepath.Join(repo.CachePath, "a.txt")); err == nil {
		t.Error("cache should be bare (no checked-out working tree)")
	}
	if repo.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", repo.DefaultBranch)
	}

	// List / Get.
	list, _ := List()
	if len(list) != 1 || list[0].ID != repo.ID {
		t.Fatalf("List = %+v, want the one repo", list)
	}
	if _, err := Get(repo.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Adding the same source again is rejected.
	if _, err := Add(AddOptions{URL: origin}); err == nil {
		t.Error("expected duplicate add to error")
	}

	// Push a new commit to the origin, then Fetch → the mirror sees it.
	os.WriteFile(filepath.Join(origin, "b.txt"), []byte("world\n"), 0644)
	run(t, origin, "add", ".")
	run(t, origin, "commit", "-q", "-m", "second")
	if err := Fetch(repo.ID); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The mirror now has b.txt in its tree.
	out := mustGit(t, repo.CachePath, "ls-tree", "--name-only", "HEAD")
	if !strings.Contains(out, "b.txt") {
		t.Errorf("fetch did not pick up new commit; tree=%q", out)
	}

	// Remove → gone from list and cache deleted.
	if err := Remove(repo.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if list, _ := List(); len(list) != 0 {
		t.Errorf("expected empty list after remove, got %+v", list)
	}
	if _, err := os.Stat(repo.CachePath); !os.IsNotExist(err) {
		t.Error("cache mirror should be deleted after Remove")
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
