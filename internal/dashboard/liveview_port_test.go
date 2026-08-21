package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/config"
)

// TestLivePortEndpoint drives GET/PUT /p/<id>/live-port through the real mux
// against a registered project, and checks validation + persistence.
func TestLivePortEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORRAL_HOME", home)

	// A registered project with a minimal config on disk, so the handler can read
	// + write config.json (InitProject needs installed assets; a direct write is
	// all this endpoint touches).
	workspace := t.TempDir()
	if err := os.MkdirAll(config.ProjectDirFor(workspace), 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := config.WriteConfig(config.ProjectDirFor(workspace), &config.ProjectConfig{Workspace: workspace}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := RegisterProject(workspace); err != nil {
		t.Fatalf("register project: %v", err)
	}
	id := ProjectID(workspace)

	srv := httptest.NewServer(newDashboardServer("tok").routes())
	defer srv.Close()
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "tok"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Unset initially → 0.
	resp := do("GET", "/p/"+id+"/live-port", "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	if got := decodePort(t, resp); got != 0 {
		t.Errorf("initial port = %d, want 0", got)
	}

	// Invalid port → 400.
	resp = do("PUT", "/p/"+id+"/live-port", `{"port":70000}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid port: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Set 1313 + a sub-path → persisted, GET returns both. The path is normalized
	// (a leading slash is added).
	resp = do("PUT", "/p/"+id+"/live-port", `{"port":1313,"path":"docs/node/"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	cfgAfter, _ := config.ReadConfig(config.ProjectDirFor(workspace))
	if cfgAfter.LiveViewPort != 1313 || cfgAfter.LiveViewPath != "/docs/node/" {
		t.Errorf("after set: port=%d path=%q, want 1313 /docs/node/", cfgAfter.LiveViewPort, cfgAfter.LiveViewPath)
	}
	resp = do("GET", "/p/"+id+"/live-port", "")
	if got := decodePort(t, resp); got != 1313 {
		t.Errorf("after set, port = %d, want 1313", got)
	}

	// Clear with 0.
	resp = do("PUT", "/p/"+id+"/live-port", `{"port":0}`)
	resp.Body.Close()
	resp = do("GET", "/p/"+id+"/live-port", "")
	if got := decodePort(t, resp); got != 0 {
		t.Errorf("after clear, port = %d, want 0", got)
	}

	// The value really landed in the project config on disk.
	cfg, err := config.ReadConfig(config.ProjectDirFor(workspace))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if cfg.LiveViewPort != 0 {
		t.Errorf("config LiveViewPort = %d, want 0 after clear", cfg.LiveViewPort)
	}
}

// TestLivePathWarning covers the health-probe heuristic: probe-like routes warn,
// real app pages don't.
func TestLivePathWarning(t *testing.T) {
	warns := []string{"/health_check", "/health_check/", "/HEALTHZ", "/up", "/ping", "/status", "/readyz"}
	for _, p := range warns {
		if livePathWarning(p) == "" {
			t.Errorf("livePathWarning(%q) = \"\", want a warning", p)
		}
	}
	ok := []string{"", "/", "/users/sign_in", "/dashboard", "/docs/node/", "/status_page/overview"}
	for _, p := range ok {
		if w := livePathWarning(p); w != "" {
			t.Errorf("livePathWarning(%q) = %q, want no warning", p, w)
		}
	}
}

// decodePort reads { "port": N } from a response body.
func decodePort(t *testing.T, resp *http.Response) int {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode port: %v", err)
	}
	return body.Port
}
