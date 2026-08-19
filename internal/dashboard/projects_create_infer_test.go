package dashboard

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInferCreateMode covers the mode inference: a semantically complete payload
// resolves without an explicit "mode", and an explicit mode always wins.
func TestInferCreateMode(t *testing.T) {
	cases := []struct {
		mode, path, name string
		repos            bool
		want             string
	}{
		{"", "", "", true, "clone"},              // repoId/repos → clone
		{"", "/some/dir", "", false, "existing"}, // path → existing
		{"", "", "scratch", false, "new"},        // name → new
		{"", "", "", false, ""},                  // nothing → unresolved
		{"new", "", "", true, "new"},             // explicit wins over inference
		{"clone", "/dir", "", false, "clone"},    // explicit wins
	}
	for _, c := range cases {
		if got := inferCreateMode(c.mode, c.path, c.name, c.repos); got != c.want {
			t.Errorf("inferCreateMode(%q,%q,%q,%v) = %q, want %q", c.mode, c.path, c.name, c.repos, got, c.want)
		}
	}
}

// TestCreateProjectEmptyPayloadError confirms an empty create returns a helpful
// 400 (not the cryptic "unknown mode"), before any filesystem/init work.
func TestCreateProjectEmptyPayloadError(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	srv := httptest.NewServer(d.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/projects/create", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty create: want 400, got %d", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("unknown mode")) || !bytes.Contains(body, []byte("nothing to create")) {
		t.Errorf("empty create should give a helpful error, got: %s", body)
	}
}
