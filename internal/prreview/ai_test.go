package prreview

import "testing"

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		"{\"a\":1}":               `{"a":1}`,
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		"  {\"a\":1}  ":           `{"a":1}`,
		"```json\n{\"a\":1}```":   `{"a":1}`,
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate100(t *testing.T) {
	if got := truncate100("short"); got != "short" {
		t.Errorf("short string changed: %q", got)
	}
	long := ""
	for i := 0; i < 150; i++ {
		long += "x"
	}
	got := truncate100(long)
	if len([]rune(got)) != 100 {
		t.Errorf("truncated len = %d runes, want 100", len([]rune(got)))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

func TestPlaceholderAnalysis(t *testing.T) {
	a := placeholderAnalysis("src/pkg/charge.ts")
	if a.Title != "Changes in charge.ts" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Importance != 3 {
		t.Errorf("importance = %d, want 3", a.Importance)
	}
}
