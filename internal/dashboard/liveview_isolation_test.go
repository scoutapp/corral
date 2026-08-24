package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHardenLiveResponse asserts the server-side half of the live-view iframe
// isolation: the framed (untrusted) app cannot dictate framing policy, and can't
// set cookies in the dashboard origin. This is security-load-bearing — embedding
// untrusted sandbox content in the dashboard must not become a sandbox→dashboard
// path — so it gets an explicit test.
func TestHardenLiveResponse(t *testing.T) {
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
	// Simulate an app that tries to forbid framing, sets its own CSP, and plants
	// a cookie — all of which we must neutralize.
	resp.Header.Set("X-Frame-Options", "DENY")
	resp.Header.Set("Content-Security-Policy", "frame-ancestors 'none'")
	resp.Header.Set("Content-Security-Policy-Report-Only", "frame-ancestors 'none'")
	resp.Header.Add("Set-Cookie", "session=abc; Path=/")

	if err := hardenLiveResponse(resp, "/p/abc/live/3000", 3000); err != nil {
		t.Fatalf("hardenLiveResponse: %v", err)
	}

	h := resp.Header
	// The app's anti-framing headers must be gone (else the browser refuses our
	// legitimate embed), replaced by our own frame-ancestors 'self'.
	if got := h.Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options should be removed, got %q", got)
	}
	if got := h.Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("CSP = %q, want frame-ancestors 'self'", got)
	}
	if got := h.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("report-only CSP should be removed, got %q", got)
	}
	// The app's cookie is now KEPT but Path-scoped to this project's mount (so
	// sessions don't bleed across projects sharing the localhost live origin) —
	// not deleted (deleting it is why login never persisted).
	sc := h.Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("expected 1 Set-Cookie, got %v", sc)
	}
	if !strings.Contains(sc[0], "session=abc") || !strings.Contains(sc[0], "Path=/p/abc/live/3000/") {
		t.Errorf("cookie should keep name=value and be Path-scoped, got %q", sc[0])
	}
}

// TestStripCookieRemovesOnlyDashboardToken verifies the request-side cookie
// hygiene: the dashboard auth cookie is removed before proxying to the app, but
// the app's own cookies pass through.
func TestStripCookieRemovesOnlyDashboardToken(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: "secret"})
	req.AddCookie(&http.Cookie{Name: "app_pref", Value: "dark"})

	stripCookie(req, dashboardCookieName)

	if _, err := req.Cookie(dashboardCookieName); err == nil {
		t.Errorf("dashboard auth cookie should be stripped before proxying")
	}
	if c, err := req.Cookie("app_pref"); err != nil || c.Value != "dark" {
		t.Errorf("app's own cookie should pass through, got %v (err %v)", c, err)
	}
}

// TestRewriteLiveHTMLBody covers the asset-path fix: a Rails-style page that
// links root-absolute assets/forms must be rewritten to carry the Live View
// mount prefix, so /assets/* resolves through the proxy (not the dashboard root,
// where it 404s → an unstyled page). External + already-prefixed URLs are left
// alone, and a <base> is injected.
func TestRewriteLiveHTMLBody(t *testing.T) {
	prefix := "/p/abc/live/3000"
	html := `<html><head><link rel="stylesheet" href="/assets/application.css">` +
		`<script src="/assets/app.js"></script></head>` +
		`<body><form action="/users/sign_in"><a href="https://cdn.example.com/x.js">ext</a>` +
		`<img src="//other/y.png"></form></body></html>`
	resp := &http.Response{
		Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader(html)),
	}
	rewriteLiveHTMLBody(resp, prefix)
	out, _ := io.ReadAll(resp.Body)
	got := string(out)

	for _, want := range []string{
		`href="` + prefix + `/assets/application.css"`,
		`src="` + prefix + `/assets/app.js"`,
		`action="` + prefix + `/users/sign_in"`,
		`<base href="` + prefix + `/">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten HTML missing %q\n---\n%s", want, got)
		}
	}
	// External + protocol-relative URLs must be untouched.
	if !strings.Contains(got, `href="https://cdn.example.com/x.js"`) {
		t.Error("external https URL should not be rewritten")
	}
	if !strings.Contains(got, `src="//other/y.png"`) {
		t.Error("protocol-relative URL should not be rewritten")
	}
	// Content-Length must be updated to the new body size.
	if resp.ContentLength != int64(len(got)) {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(got))
	}
}

// TestRewriteLiveHTMLBodyIdempotent: rewriting an already-prefixed page is a
// no-op on the paths (so a double-pass can't double-prefix).
func TestRewriteLiveHTMLBodyIdempotent(t *testing.T) {
	prefix := "/p/abc/live/3000"
	html := `<html><head><base href="` + prefix + `/"><link href="` + prefix + `/assets/a.css"></head><body></body></html>`
	resp := &http.Response{
		Header: http.Header{"Content-Type": {"text/html"}},
		Body:   io.NopCloser(strings.NewReader(html)),
	}
	rewriteLiveHTMLBody(resp, prefix)
	out, _ := io.ReadAll(resp.Body)
	if got := string(out); strings.Contains(got, prefix+prefix) {
		t.Errorf("double-prefixed a path: %s", got)
	}
}

// TestRewriteLocationHeader: redirects to localhost:<port> or root-absolute get
// the mount prefix; external redirects are left alone.
func TestRewriteLocationHeader(t *testing.T) {
	prefix := "/p/abc/live/3000"
	cases := []struct{ in, want string }{
		{"http://localhost:3000/users/sign_in", prefix + "/users/sign_in"},
		{"http://127.0.0.1:3000/home", prefix + "/home"},
		{"/dashboard", prefix + "/dashboard"},
		{prefix + "/already", prefix + "/already"}, // already prefixed → unchanged
		{"https://accounts.google.com/o/oauth2", "https://accounts.google.com/o/oauth2"},
		{"//cdn.example.com/x", "//cdn.example.com/x"},
	}
	for _, c := range cases {
		h := http.Header{"Location": {c.in}}
		rewriteLocationHeader(h, prefix, 3000)
		if got := h.Get("Location"); got != c.want {
			t.Errorf("rewriteLocationHeader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRewriteLiveSetCookies covers the two-container fix: each app cookie is
// Path-scoped to the project mount and any Domain stripped, so projects sharing
// the one localhost live origin don't clobber each other's sessions.
func TestRewriteLiveSetCookies(t *testing.T) {
	scope := "/p/proj-a/live/3000"
	cases := []struct {
		name     string
		in       string
		wantHas  []string
		wantMiss []string
	}{
		{"replaces existing Path=/", "s=1; Path=/; HttpOnly",
			[]string{"s=1", "Path=/p/proj-a/live/3000/", "HttpOnly"}, []string{"Path=/;"}},
		{"adds Path when missing", "s=1; HttpOnly; SameSite=Lax",
			[]string{"s=1", "Path=/p/proj-a/live/3000/", "HttpOnly", "SameSite=Lax"}, nil},
		{"drops Domain", "s=1; Domain=evil.example.com; Path=/",
			[]string{"s=1", "Path=/p/proj-a/live/3000/"}, []string{"Domain="}},
		{"case-insensitive attrs", "s=1; PATH=/; DOMAIN=x.com",
			[]string{"Path=/p/proj-a/live/3000/"}, []string{"DOMAIN=", "x.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := forcePathAndDropDomain(c.in, scope)
			for _, w := range c.wantHas {
				if !strings.Contains(got, w) {
					t.Errorf("%q missing %q", got, w)
				}
			}
			for _, w := range c.wantMiss {
				if strings.Contains(got, w) {
					t.Errorf("%q should not contain %q", got, w)
				}
			}
		})
	}

	// Multiple Set-Cookie headers → each rewritten.
	h := http.Header{}
	h.Add("Set-Cookie", "a=1; Path=/")
	h.Add("Set-Cookie", "b=2")
	rewriteLiveSetCookies(h, scope)
	sc := h.Values("Set-Cookie")
	if len(sc) != 2 {
		t.Fatalf("expected 2 cookies, got %v", sc)
	}
	for _, c := range sc {
		if !strings.Contains(c, "Path=/p/proj-a/live/3000/") {
			t.Errorf("cookie not path-scoped: %q", c)
		}
	}
}

// TestRequireLiveAuth: the live listener accepts ONLY liveToken (via ?__live_token
// bootstrap or the corral_live_token cookie), never the dashboard token.
func TestRequireLiveAuth(t *testing.T) {
	d := &dashboardServer{liveToken: "LIVE", token: "DASH", apiToken: "API"}
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) }
	h := d.requireLiveAuth(ok)

	// Valid ?__live_token → 302 redirect that sets the cookie and strips the param.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/p/x/live/3000/?__live_token=LIVE", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("bootstrap: status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "__live_token") {
		t.Errorf("redirect should strip the token param, got %q", loc)
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, liveCookieName+"=LIVE") || !strings.Contains(sc, "HttpOnly") {
		t.Errorf("should set the HttpOnly live cookie, got %q", sc)
	}

	// Valid cookie → passes through.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/p/x/live/3000/", nil)
	req.AddCookie(&http.Cookie{Name: liveCookieName, Value: "LIVE"})
	h(rec, req)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("valid cookie: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// The DASHBOARD token must NOT authenticate here (neither as cookie nor param).
	for _, bad := range []*http.Request{
		func() *http.Request {
			r := httptest.NewRequest("GET", "/p/x/live/3000/", nil)
			r.AddCookie(&http.Cookie{Name: liveCookieName, Value: "DASH"})
			return r
		}(),
		httptest.NewRequest("GET", "/p/x/live/3000/?__live_token=DASH", nil),
		httptest.NewRequest("GET", "/p/x/live/3000/", nil), // no creds at all
	} {
		rec = httptest.NewRecorder()
		h(rec, bad)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for %q, got %d", bad.URL, rec.Code)
		}
	}
}
