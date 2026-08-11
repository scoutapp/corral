package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeRepo initializes a minimal git repo with one commit at dir.
func makeRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@e.com"},
		{"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestGitReposSiblings covers the "parent dir isn't a repo, children are"
// layout: /git/repos should detect both child repos and not the parent.
func TestGitReposSiblings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)
	ws := t.TempDir()
	makeRepo(t, filepath.Join(ws, "api"))
	makeRepo(t, filepath.Join(ws, "web"))
	os.MkdirAll(filepath.Join(ws, "docs"), 0755) // not a repo

	if err := RegisterProject(ws); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}
	id := ProjectID(ws)
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/p/"+id+"/git/repos", nil)
	req.AddCookie(&http.Cookie{Name: "corral_dash_token", Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		RootIsRepo bool `json:"rootIsRepo"`
		Repos      []struct {
			Path string `json:"path"`
		} `json:"repos"`
	}
	json.NewDecoder(resp.Body).Decode(&out)

	if out.RootIsRepo {
		t.Error("rootIsRepo should be false for a non-repo parent")
	}
	got := map[string]bool{}
	for _, rp := range out.Repos {
		got[rp.Path] = true
	}
	if !got["api"] || !got["web"] {
		t.Errorf("expected api+web repos, got %+v", out.Repos)
	}
	if got["docs"] {
		t.Error("docs is not a repo and must not be listed")
	}
}

// TestGitRepoDirGuard confirms the repo param can't escape the workspace.
func TestGitRepoDirGuard(t *testing.T) {
	ws := t.TempDir()
	// safeJoin treats the param as workspace-relative and clamps "../" so the
	// result can never leave the workspace — a "../escape" collapses to
	// <ws>/escape, not an escape. Assert the resolved dir always stays inside ws.
	for _, repo := range []string{"", "api", "../escape", "../../etc", "a/b/c"} {
		r, _ := http.NewRequest("GET", "/x?repo="+repo, nil)
		dir, ok := gitRepoDir(ws, r)
		if !ok {
			t.Errorf("gitRepoDir(repo=%q) unexpectedly not ok", repo)
			continue
		}
		if dir != ws && !filepathHasPrefix(dir, ws) {
			t.Errorf("gitRepoDir(repo=%q) = %q escaped workspace %q", repo, dir, ws)
		}
	}
}

func filepathHasPrefix(p, prefix string) bool {
	return len(p) >= len(prefix) && p[:len(prefix)] == prefix
}
