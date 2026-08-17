package dashboard

import (
	"net/http"
	"testing"
)

// TestHardenLiveResponse asserts the server-side half of the live-view iframe
// isolation: the framed (untrusted) app cannot dictate framing policy, and can't
// set cookies in the dashboard origin. This is security-load-bearing — embedding
// untrusted sandbox content in the dashboard must not become a sandbox→dashboard
// path — so it gets an explicit test.
func TestHardenLiveResponse(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	// Simulate an app that tries to forbid framing, sets its own CSP, and plants
	// a cookie — all of which we must neutralize.
	resp.Header.Set("X-Frame-Options", "DENY")
	resp.Header.Set("Content-Security-Policy", "frame-ancestors 'none'")
	resp.Header.Set("Content-Security-Policy-Report-Only", "frame-ancestors 'none'")
	resp.Header.Add("Set-Cookie", "session=abc; Path=/")

	if err := hardenLiveResponse(resp); err != nil {
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
	// The framed app must not set cookies in the dashboard origin.
	if got := h.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie should be stripped, got %v", got)
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
