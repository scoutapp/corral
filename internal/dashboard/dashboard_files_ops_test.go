package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesMutations(t *testing.T) {
	t.Setenv("SANDCLAUDE_HOME", t.TempDir())
	ws := t.TempDir()
	if err := RegisterProject(ws); err != nil {
		t.Fatal(err)
	}
	id := ProjectID(ws)
	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()

	do := func(method, path string) int {
		req, _ := http.NewRequest(method, srv.URL+"/p/"+id+path, nil)
		req.AddCookie(&http.Cookie{Name: "sc_dash_token", Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// mkdir
	if s := do("POST", "/files/mkdir?path=sub/dir"); s != 200 {
		t.Fatalf("mkdir status %d", s)
	}
	if fi, err := os.Stat(filepath.Join(ws, "sub", "dir")); err != nil || !fi.IsDir() {
		t.Error("mkdir did not create the dir")
	}

	// new file
	if s := do("POST", "/files/new?path=sub/a.txt"); s != 200 {
		t.Fatalf("new status %d", s)
	}
	if _, err := os.Stat(filepath.Join(ws, "sub", "a.txt")); err != nil {
		t.Error("new did not create the file")
	}
	// new again → conflict
	if s := do("POST", "/files/new?path=sub/a.txt"); s != http.StatusConflict {
		t.Errorf("duplicate new status = %d, want 409", s)
	}

	// rename
	if s := do("POST", "/files/rename?from=sub/a.txt&to=sub/b.txt"); s != 200 {
		t.Fatalf("rename status %d", s)
	}
	if _, err := os.Stat(filepath.Join(ws, "sub", "b.txt")); err != nil {
		t.Error("rename target missing")
	}
	if _, err := os.Stat(filepath.Join(ws, "sub", "a.txt")); !os.IsNotExist(err) {
		t.Error("rename source should be gone")
	}

	// delete (the dir, recursively)
	if s := do("DELETE", "/files?path=sub"); s != 200 {
		t.Fatalf("delete status %d", s)
	}
	if _, err := os.Stat(filepath.Join(ws, "sub")); !os.IsNotExist(err) {
		t.Error("delete did not remove the dir")
	}

	// --- safety guards ---
	// "../" is clamped inside the workspace (not an escape): safeJoin turns
	// "../escaped" into <ws>/escaped. Assert nothing was created OUTSIDE the ws.
	do("POST", "/files/mkdir?path=../escaped")
	if _, err := os.Stat(filepath.Join(filepath.Dir(ws), "escaped")); !os.IsNotExist(err) {
		t.Error("mkdir created a dir OUTSIDE the workspace (escaped)")
	}
	// can't delete the workspace root itself (empty path).
	if s := do("DELETE", "/files?path="); s == 200 {
		t.Error("delete of workspace root should be refused")
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatal("workspace root was deleted!")
	}
	// wrong method.
	if s := do("GET", "/files/mkdir?path=x"); s != http.StatusMethodNotAllowed {
		t.Errorf("GET mkdir status = %d, want 405", s)
	}
}
