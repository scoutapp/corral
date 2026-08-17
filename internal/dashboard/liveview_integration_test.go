package dashboard

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveTunnelAgainstContainer is an OPT-IN integration test: it tunnels an
// HTTP GET into a real running corral container via dialExecTunnel and checks the
// bytes come back. It needs a container running a server on 127.0.0.1:<port>;
// point it at one with:
//
//	CORRAL_LIVE_TEST_CONTAINER=<name> CORRAL_LIVE_TEST_PORT=9998 go test ./internal/dashboard -run TestLiveTunnelAgainstContainer -v
//
// Without those env vars it skips, so CI (no container) stays green.
func TestLiveTunnelAgainstContainer(t *testing.T) {
	container := os.Getenv("CORRAL_LIVE_TEST_CONTAINER")
	port := os.Getenv("CORRAL_LIVE_TEST_PORT")
	if container == "" || port == "" {
		t.Skip("set CORRAL_LIVE_TEST_CONTAINER and CORRAL_LIVE_TEST_PORT to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	p, _ := parsePortForTest(port)
	conn, err := dialExecTunnel(ctx, container, p)
	if err != nil {
		t.Fatalf("dialExecTunnel: %v", err)
	}
	defer conn.Close()

	req := "GET / HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf)
	if !strings.HasPrefix(got, "HTTP/") {
		t.Fatalf("expected an HTTP response, got %q", got[:min(len(got), 80)])
	}
	if sc := got[:strings.IndexByte(got, '\n')]; !strings.Contains(sc, " 200") && !strings.Contains(sc, " 30") && !strings.Contains(sc, " 40") {
		t.Logf("status line: %s", strings.TrimSpace(sc))
	}
	t.Logf("tunnel returned %d bytes, status: %s", len(buf), strings.TrimSpace(got[:strings.IndexByte(got, '\n')]))
}

// TestLiveProxyAgainstContainer drives the full reverse-proxy (not just the raw
// tunnel) against a live container by name, checking a GET comes back with the
// app's body. Opt-in via the same container/port env vars.
func TestLiveProxyAgainstContainer(t *testing.T) {
	container := os.Getenv("CORRAL_LIVE_TEST_CONTAINER")
	port := os.Getenv("CORRAL_LIVE_TEST_PORT")
	if container == "" || port == "" {
		t.Skip("set CORRAL_LIVE_TEST_CONTAINER and CORRAL_LIVE_TEST_PORT to run")
	}
	p, _ := parsePortForTest(port)

	req := httptest.NewRequest("GET", "/p/x/live/"+port+"/lv.txt", nil)
	rec := httptest.NewRecorder()
	liveProxyTo(rec, req, container, "x", p, "lv.txt")

	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	t.Logf("proxy status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	if res.StatusCode != 200 {
		t.Fatalf("expected 200 from proxied app, got %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "hello-from-container") {
		t.Fatalf("proxied body missing expected content: %q", body)
	}
}

func parsePortForTest(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
