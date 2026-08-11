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

// TestReposEndpoints exercises the /repos routes against the real mux: add a
// repo (from a local origin, no network), list it, then delete it.
func TestReposEndpoints(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())

	// Local origin repo to clone.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = origin
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(origin, "f.txt"), []byte("x\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = origin
		c.CombinedOutput()
	}

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// POST /repos — add.
	resp := do("POST", "/repos", `{"url":"`+origin+`","name":"demo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /repos status = %d", resp.StatusCode)
	}
	var added struct {
		Repo struct{ ID, Name string } `json:"repo"`
	}
	json.NewDecoder(resp.Body).Decode(&added)
	resp.Body.Close()
	if added.Repo.ID == "" || added.Repo.Name != "demo" {
		t.Fatalf("unexpected add result: %+v", added)
	}

	// GET /repos — list contains it.
	resp = do("GET", "/repos", "")
	var listed struct {
		Repos []struct{ ID string } `json:"repos"`
	}
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Repos) != 1 || listed.Repos[0].ID != added.Repo.ID {
		t.Fatalf("list = %+v, want the added repo", listed)
	}

	// DELETE /repos/<id> — remove.
	resp = do("DELETE", "/repos/"+added.Repo.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do("GET", "/repos", "")
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Repos) != 0 {
		t.Errorf("expected empty list after delete, got %+v", listed)
	}
}
