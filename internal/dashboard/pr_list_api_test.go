//go:build sqlite_fts5

package dashboard

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestAPIRepoPRsList verifies the cached-PR list endpoint is on /api and returns
// each PR's INTERNAL id — the key the other /api/prs/{id}/* endpoints need. (The
// inbox / open endpoints call gh, so they're covered by drift, not here.)
func TestAPIRepoPRsList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)
	// handleRepoPRs validates the repo exists in the repos registry; write a
	// minimal repos.json so "repoA" is known (avoids a real clone).
	if err := os.WriteFile(filepath.Join(config.CorralHome(), "repos.json"),
		[]byte(`{"repos":[{"id":"repoA","name":"acme/widget","url":"https://github.com/acme/widget"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newDashboardServer("sess")
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	// Seed a cached PR for a repo.
	res, err := s.DB().Exec(
		`INSERT INTO prs (repo_id, pr_number, title, fetched_at) VALUES (?,?,?,datetime('now'))`,
		"repoA", 7, "Fix the thing")
	if err != nil {
		t.Fatal(err)
	}
	prID, _ := res.LastInsertId()

	srv := httptest.NewServer(d.routes())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/repos/repoA/prs", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/repos/repoA/prs: %d %s", resp.StatusCode, body)
	}
	// The response must expose the internal id so callers can act on the PR.
	if !bytes.Contains(body, []byte(`"id":`+strconv.FormatInt(prID, 10))) {
		t.Fatalf("cached PR list missing internal id %d: %s", prID, body)
	}
	if !bytes.Contains(body, []byte("Fix the thing")) {
		t.Fatalf("cached PR list missing title: %s", body)
	}
}
