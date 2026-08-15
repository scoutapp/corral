package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseProxyLogTime(t *testing.T) {
	line := "2026/07/28 01:44:43 ALLOWED  api.anthropic.com:443"
	ts, ok := parseProxyLogTime(line)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	want := time.Date(2026, 7, 28, 1, 44, 43, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("got %v, want %v", ts, want)
	}

	if _, ok := parseProxyLogTime("garbage"); ok {
		t.Error("expected parse to fail on garbage")
	}
	if _, ok := parseProxyLogTime(""); ok {
		t.Error("expected parse to fail on empty")
	}
}

func TestCountRecentAnthropicHits(t *testing.T) {
	// Fixed reference time in UTC (proxy.log timestamps are UTC; callers pass
	// time.Now().UTC()).
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fmtT := func(offset time.Duration) string {
		return now.Add(offset).Format("2006/01/02 15:04:05")
	}

	// A working burst (4 hits in last 40s) + old hits + non-anthropic noise.
	content := "" +
		fmtT(-5*time.Second) + " ALLOWED  api.anthropic.com:443\n" +
		fmtT(-15*time.Second) + " ALLOWED  api.anthropic.com:443\n" +
		fmtT(-25*time.Second) + "   - api.anthropic.com\n" + // passthrough form
		fmtT(-40*time.Second) + " ALLOWED  api.anthropic.com:443\n" +
		fmtT(-10*time.Second) + " ALLOWED  http-intake.logs.us5.datadoghq.com:443\n" + // not anthropic
		fmtT(-5*time.Minute) + " ALLOWED  api.anthropic.com:443\n" // outside 60s window

	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got := countRecentAnthropicHits(path, now, 60*time.Second)
	if got != 4 {
		t.Errorf("got %d hits, want 4 (4 anthropic within 60s; datadog + 5-min-old excluded)", got)
	}

	// Empty/missing file => 0, no panic.
	if countRecentAnthropicHits(filepath.Join(dir, "nope.log"), now, 60*time.Second) != 0 {
		t.Error("missing file should yield 0 hits")
	}
}

func TestProjectActivityGate(t *testing.T) {
	// Container down => off regardless of logs.
	act, hits := projectActivity(t.TempDir(), false, false, 0)
	if act != "off" || hits != 0 {
		t.Errorf("container down: got %q/%d, want off/0", act, hits)
	}
}

func TestFallbackBurstSuppression(t *testing.T) {
	// With no mitm port (0), projectActivity falls back to proxy.log with the
	// higher fallback threshold, so a short auto-completion burst (a handful of
	// hits) stays "waiting" while a sustained burst goes "working".
	now := time.Now().UTC()
	fmtT := func(off time.Duration) string { return now.Add(off).Format("2006/01/02 15:04:05") }
	write := func(n int) string {
		ws := t.TempDir()
		logDir := logsDirForWorkspace(ws) // <ws>/.corral/logs — where projectActivity reads
		os.MkdirAll(logDir, 0755)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(fmtT(-time.Duration(i) * time.Second))
			b.WriteString(" ALLOWED  api.anthropic.com:443\n")
		}
		os.WriteFile(filepath.Join(logDir, "proxy.log"), []byte(b.String()), 0644)
		return ws
	}

	// A 5-hit burst (auto-completion-sized) is below activityFallbackN → waiting.
	small := write(5)
	if act, _ := projectActivity(small, true, true, 0); act != "waiting" {
		t.Errorf("5-hit fallback burst: got %q, want waiting (suppressed)", act)
	}
	// A 12-hit sustained burst clears the fallback threshold → working.
	big := write(12)
	if act, _ := projectActivity(big, true, true, 0); act != "working" {
		t.Errorf("12-hit fallback burst: got %q, want working", act)
	}
}

func TestMessageFlowCounting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recent := float64(now.Add(-10 * time.Second).Unix())
	old := float64(now.Add(-5 * time.Minute).Unix())

	// A stub mitmweb /flows endpoint: 2 recent POST /v1/messages, plus a
	// token-count call, a statsig call, and an old completion — only the 2 recent
	// /v1/messages should count.
	body := `[
	  {"request":{"method":"POST","pretty_host":"api.anthropic.com","path":"/v1/messages","timestamp_start":` + ftoa(recent) + `}},
	  {"request":{"method":"POST","pretty_host":"api.anthropic.com","path":"/v1/messages","timestamp_start":` + ftoa(recent) + `}},
	  {"request":{"method":"POST","pretty_host":"api.anthropic.com","path":"/v1/messages/count_tokens","timestamp_start":` + ftoa(recent) + `}},
	  {"request":{"method":"POST","pretty_host":"statsig.anthropic.com","path":"/v1/log_event","timestamp_start":` + ftoa(recent) + `}},
	  {"request":{"method":"POST","pretty_host":"api.anthropic.com","path":"/v1/messages","timestamp_start":` + ftoa(old) + `}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flows" {
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	n, ok := countRecentMessageFlows(port, now, 60*time.Second)
	if !ok {
		t.Fatal("expected ok=true (anthropic flows present)")
	}
	if n != 2 {
		t.Errorf("got %d recent /v1/messages, want 2 (token-count, statsig, old excluded)", n)
	}
}

func TestMessageFlowFallsBackWhenNoAnthropic(t *testing.T) {
	// No anthropic flows at all → can't tell idle from not-decrypted → ok=false,
	// signaling the caller to fall back to proxy.log.
	body := `[{"request":{"method":"GET","pretty_host":"github.com","path":"/","timestamp_start":1}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	_, ok := countRecentMessageFlows(portOf(t, srv.URL), time.Now(), 60*time.Second)
	if ok {
		t.Error("expected ok=false when no anthropic flows are present")
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
