package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupHome points SANDCLAUDE_HOME at a temp dir with a minimal asset layout so
// InitProject can seed the allowlist (config.AssetsDir resolves under it).
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SANDCLAUDE_HOME", home)
	sandbox := filepath.Join(home, "assets", "sandbox", "allowlist-proxy")
	os.MkdirAll(sandbox, 0755)
	os.WriteFile(filepath.Join(home, "assets", "sandbox", "Dockerfile"), []byte("FROM scratch\n"), 0644)
	os.WriteFile(filepath.Join(sandbox, "allowed-domains.txt"), []byte("api.anthropic.com\n"), 0644)
	return home
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.CombinedOutput()
	}
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.CombinedOutput()
	}
}

func TestCreateProject(t *testing.T) {
	setupHome(t)
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()
	post := func(path, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return resp, out
	}

	// mode=new → makes a managed workspace + config + registers.
	resp, out := post("/projects/create", `{"mode":"new","name":"fresh proj","proxy":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new: status %d (%v)", resp.StatusCode, out)
	}
	ws, _ := out["workspace"].(string)
	if _, err := os.Stat(filepath.Join(ws, ".sandclaude", "project", "config.json")); err != nil {
		t.Errorf("new: config not written: %v", err)
	}
	if out["id"] == "" {
		t.Error("new: no project id returned")
	}

	// mode=existing → registers an already-present dir.
	existing := t.TempDir()
	resp, out = post("/projects/create", `{"mode":"existing","path":"`+existing+`","proxy":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("existing: status %d (%v)", resp.StatusCode, out)
	}
	if _, err := os.Stat(filepath.Join(existing, ".sandclaude", "project", "config.json")); err != nil {
		t.Errorf("existing: config not written: %v", err)
	}

	// mode=clone → clone --local from a repo's cache.
	origin := t.TempDir()
	gitInit(t, origin)
	// add the repo first
	_, radd := post("/repos", `{"url":"`+origin+`","name":"cloneme"}`)
	repoMap, _ := radd["repo"].(map[string]any)
	repoID, _ := repoMap["id"].(string)
	if repoID == "" {
		t.Fatalf("repo add failed: %v", radd)
	}
	// Single-repo clone (legacy repoId): repo lands in a SUBDIR of the parent
	// workspace (parent-dir layout), which is the container's workspace.
	resp, out = post("/projects/create", `{"mode":"clone","repoId":"`+repoID+`","proxy":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clone: status %d (%v)", resp.StatusCode, out)
	}
	cloneWs, _ := out["workspace"].(string)
	if _, err := os.Stat(filepath.Join(cloneWs, "cloneme", "f.txt")); err != nil {
		t.Errorf("clone: expected origin file in subdir cloneme/f.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneWs, "cloneme", ".git")); err != nil {
		t.Errorf("clone: subdir should have its own .git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneWs, ".sandclaude", "project", "config.json")); err != nil {
		t.Errorf("clone: config not written at parent: %v", err)
	}

	// Multi-repo clone: two repos into two subdirs of one parent workspace.
	o2 := t.TempDir()
	gitInit(t, o2)
	_, r2 := post("/repos", `{"url":"`+o2+`","name":"second"}`)
	rid2, _ := (r2["repo"].(map[string]any))["id"].(string)
	resp, out = post("/projects/create",
		`{"mode":"clone","name":"multi","proxy":false,"repos":[{"repoId":"`+repoID+`"},{"repoId":"`+rid2+`"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multi clone: status %d (%v)", resp.StatusCode, out)
	}
	multiWs, _ := out["workspace"].(string)
	for _, sub := range []string{"cloneme", "second"} {
		if _, err := os.Stat(filepath.Join(multiWs, sub, ".git")); err != nil {
			t.Errorf("multi: expected subdir %s with its own .git: %v", sub, err)
		}
	}
	if _, err := os.Stat(filepath.Join(multiWs, ".sandclaude", "project", "config.json")); err != nil {
		t.Errorf("multi: config not written at parent: %v", err)
	}
}
