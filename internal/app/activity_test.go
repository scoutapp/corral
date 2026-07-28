package app

import (
	"os"
	"path/filepath"
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
	act, hits := projectActivity(t.TempDir(), false, false)
	if act != "off" || hits != 0 {
		t.Errorf("container down: got %q/%d, want off/0", act, hits)
	}
}
