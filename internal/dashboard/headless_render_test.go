package dashboard

import "testing"

// TestRenderedTextLen: a blank SPA shell yields ~0 visible text (the white-screen
// signal), while a page with real content clears the threshold. Script/style
// contents must not count as visible text.
func TestRenderedTextLen(t *testing.T) {
	// Blank SPA shell — empty root, only scripts. Should be ~0.
	blank := `<!doctype html><html><head><style>body{margin:0}</style>` +
		`<script>window.__x=1;var a="lots of code here that should not count as text";</script></head>` +
		`<body><div id="root"></div><script src="/app.js"></script></body></html>`
	if n := renderedTextLen(blank); n > 20 {
		t.Errorf("blank SPA should have ~no visible text, got %d", n)
	}

	// Real content.
	real := `<html><body><h1>Welcome to Acme</h1><p>This is the actual rendered page ` +
		`with plenty of visible words a human would read.</p></body></html>`
	if n := renderedTextLen(real); n < 40 {
		t.Errorf("real page should clear the threshold, got %d", n)
	}

	if renderedTextLen("") != 0 {
		t.Errorf("empty input should be 0")
	}
}

// TestFindHostBrowser honors the explicit override env var.
func TestFindHostBrowser(t *testing.T) {
	// A path that exists (this test binary) stands in for a browser path.
	self := "/bin/sh"
	t.Setenv("CORRAL_HEADLESS_BROWSER", self)
	if got := findHostBrowser(); got != self {
		t.Errorf("override not honored: got %q want %q", got, self)
	}
	// A non-existent override is ignored (falls through to detection).
	t.Setenv("CORRAL_HEADLESS_BROWSER", "/no/such/browser")
	if got := findHostBrowser(); got == "/no/such/browser" {
		t.Errorf("non-existent override should be ignored, got %q", got)
	}
}
