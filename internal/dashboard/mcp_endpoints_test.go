package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/mcp"
)

// newMCPTestServer builds a dashboard whose mcp client is a fake driven by the
// given runner, so /api/mcp handlers don't shell out to the real `claude mcp`.
func newMCPTestServer(t *testing.T, run func(ctx context.Context, args ...string) (string, error)) (*httptest.Server, *[]string) {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	d.apiToken = "apitok"
	var lastArgs []string
	d.mcpClientOverride = mcp.NewWithRunner(func(ctx context.Context, args ...string) (string, error) {
		lastArgs = args
		return run(ctx, args...)
	})
	return httptest.NewServer(d.routes()), &lastArgs
}

const mcpListSample = `Checking MCP server health…

sentry: https://mcp.sentry.dev/mcp - ! Needs authentication
linear: https://mcp.linear.app/sse - ✔ Connected
`

func TestMCPListEndpoint(t *testing.T) {
	srv, _ := newMCPTestServer(t, func(ctx context.Context, args ...string) (string, error) {
		return mcpListSample, nil
	})
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/mcp", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Servers []mcp.Server `json:"servers"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out.Servers))
	}
	if out.Servers[0].Name != "sentry" || out.Servers[0].Status != mcp.StatusNeedsAuth {
		t.Errorf("server[0] wrong: %+v", out.Servers[0])
	}
	if out.Servers[1].Transport != mcp.TransportSSE {
		t.Errorf("linear transport = %q, want sse", out.Servers[1].Transport)
	}
}

func TestMCPAddEndpoint(t *testing.T) {
	srv, lastArgs := newMCPTestServer(t, func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	})
	defer srv.Close()

	body := `{"name":"sentry","transport":"http","url":"https://mcp.sentry.dev/mcp","header":"Authorization: Bearer xyz"}`
	req, _ := http.NewRequest("POST", srv.URL+"/api/mcp", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("add status = %d", resp.StatusCode)
	}
	joined := strings.Join(*lastArgs, " ")
	for _, want := range []string{"mcp add", "--scope user", "--transport http", "sentry", "https://mcp.sentry.dev/mcp"} {
		if !strings.Contains(joined, want) {
			t.Errorf("add didn't run expected command; missing %q in %s", want, joined)
		}
	}
}

func TestMCPRemoveEndpoint(t *testing.T) {
	srv, lastArgs := newMCPTestServer(t, func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	})
	defer srv.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/mcp/sentry", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "sess"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("remove status = %d", resp.StatusCode)
	}
	if strings.Join(*lastArgs, " ") != "mcp remove sentry" {
		t.Errorf("remove args = %v", *lastArgs)
	}
}

// The write gate governs Claude/CLI (API token), not the browser. A POST with the
// API token 403s when writes are off; the browser (session token) always works.
func TestMCPWriteGate(t *testing.T) {
	srv, _ := newMCPTestServer(t, func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	})
	defer srv.Close()

	post := func(token string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/mcp", strings.NewReader(`{"name":"x","url":"https://x/mcp"}`))
		req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}

	// API token + writes disabled (default) → 403 (this is Claude, gated).
	if code := post("apitok"); code != http.StatusForbidden {
		t.Errorf("api-token add with writes off: got %d, want 403", code)
	}
	// Browser session token → never gated, reaches the handler (200).
	if code := post("sess"); code == http.StatusForbidden {
		t.Errorf("browser add was gated (403) — the user must never be gated")
	}
}
