//go:build sqlite_fts5

package dashboard

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestAPIPRLinks drives the /api/prs/<id>/links surface: list (empty), add
// (validation + success), round-trip, and delete.
func TestAPIPRLinks(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	// Seed two PRs to link.
	mk := func(num int) int64 {
		res, err := s.DB().Exec(
			`INSERT INTO prs (repo_id, pr_number, title, fetched_at) VALUES (?,?,?,datetime('now'))`,
			"repo1", num, "PR")
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	a, b := mk(1), mk(2)

	srv := httptest.NewServer(d.routes())
	defer srv.Close()
	req := func(method, path, body string) (int, []byte) {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rq, _ := http.NewRequest(method, srv.URL+path, r)
		rq.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		bb, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, bb
	}

	base := "/api/prs/" + strconv.FormatInt(a, 10) + "/links"

	// Empty to start.
	if code, body := req("GET", base, ""); code != 200 || !bytes.Contains(body, []byte(`"links"`)) {
		t.Fatalf("GET links: %d %s", code, body)
	}
	// Missing linkedPrId → 400.
	if code, _ := req("POST", base, `{}`); code != 400 {
		t.Fatalf("POST no linkedPrId: want 400, got %d", code)
	}
	// Add a link.
	code, body := req("POST", base, `{"linkedPrId":`+strconv.FormatInt(b, 10)+`,"relationship":"depends_on","note":"needs schema"}`)
	if code != 200 || !bytes.Contains(body, []byte(`"depends_on"`)) {
		t.Fatalf("POST link: %d %s", code, body)
	}
	// It shows up.
	_, body = req("GET", base, "")
	if !bytes.Contains(body, []byte(`"linkedPrId":`+strconv.FormatInt(b, 10))) {
		t.Fatalf("link not listed: %s", body)
	}
	// Suggest resolves (b shares no diff so it may be empty, but the route works).
	if code, _ := req("GET", base+"/suggest", ""); code != 200 {
		t.Fatalf("suggest: want 200, got %d", code)
	}

	// Extract the link id from the list, then delete it.
	linkID := extractFirstLinkID(t, body)
	if code, _ := req("DELETE", base+"/"+strconv.FormatInt(linkID, 10), ""); code != 200 {
		t.Fatalf("DELETE link: want 200, got %d", code)
	}
	_, body = req("GET", base, "")
	if bytes.Contains(body, []byte(`"linkedPrId":`+strconv.FormatInt(b, 10))) {
		t.Fatalf("link should be gone after delete: %s", body)
	}
}

// extractFirstLinkID pulls the first "id":<n> out of the links JSON (good enough
// for the test without a full decode).
func extractFirstLinkID(t *testing.T, body []byte) int64 {
	t.Helper()
	i := bytes.Index(body, []byte(`"id":`))
	if i < 0 {
		t.Fatalf("no link id in %s", body)
	}
	j := i + len(`"id":`)
	k := j
	for k < len(body) && body[k] >= '0' && body[k] <= '9' {
		k++
	}
	id, err := strconv.ParseInt(string(body[j:k]), 10, 64)
	if err != nil {
		t.Fatalf("bad link id: %v", err)
	}
	return id
}
