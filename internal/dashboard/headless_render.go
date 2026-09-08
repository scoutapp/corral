package dashboard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Host-side headless render check. Verifying that a Live View page ACTUALLY
// renders (not a white screen) can only be done from the host: the live proxy is
// a host loopback listener gated by the live token, and the sandbox can't reach
// the dashboard. curl is not enough — it never runs the page's JS. So we drive a
// headless browser already on the host at the real live-proxy URL and measure the
// rendered DOM.
//
// We deliberately do NOT add a browser dependency: we use whatever Chrome/Chromium
// the host already has (Chrome on macOS, chromium/google-chrome on Linux), via
// Chrome's built-in `--headless --dump-dom`/`--screenshot`. If none is found we
// report that clearly rather than failing hard.

// renderResult is what a headless render check produced.
type renderResult struct {
	BrowserFound bool   `json:"browserFound"`
	Browser      string `json:"browser,omitempty"`    // path of the browser used
	DOMTextLen   int    `json:"domTextLen"`           // visible-ish text length in the rendered DOM
	Screenshot   string `json:"screenshot,omitempty"` // host path to a PNG screenshot, when captured
	Err          string `json:"error,omitempty"`      // non-fatal note (browser missing, timeout, …)
}

// findHostBrowser returns the path to an installed Chrome/Chromium, or "".
func findHostBrowser() string {
	// Explicit override wins (tests, unusual installs).
	if p := strings.TrimSpace(os.Getenv("CORRAL_HEADLESS_BROWSER")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	}
	// PATH names (Linux, and mac if installed via a package).
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

var htmlTagRE = regexp.MustCompile(`(?s)<[^>]+>`)
var wsRE = regexp.MustCompile(`\s+`)

// headlessRender loads url in a headless browser, returning the rendered DOM's
// approximate visible-text length and a screenshot (best-effort). It follows the
// live-token redirect and runs JS, so a blank SPA yields a near-zero text length —
// which is how a white screen is caught. ctx bounds the whole thing.
func (d *dashboardServer) headlessRender(ctx context.Context, url string) renderResult {
	browser := findHostBrowser()
	if browser == "" {
		return renderResult{BrowserFound: false, Err: "no Chrome/Chromium found on the host to render-check the page (set CORRAL_HEADLESS_BROWSER to a browser path)"}
	}
	res := renderResult{BrowserFound: true, Browser: browser}

	shotDir, err := os.MkdirTemp("", "corral-liveview-")
	if err == nil {
		res.Screenshot = filepath.Join(shotDir, "live.png")
	}

	// Common flags: fully headless, no sandbox (may run as a service user), a fixed
	// window so the screenshot is sensible, and a virtual-time budget so SPA JS runs
	// then the DOM is dumped.
	base := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--hide-scrollbars",
		"--window-size=1280,900",
		"--virtual-time-budget=8000", // let JS run ~8s before dumping
	}

	// 1) Dump the rendered DOM to measure real content.
	domArgs := append(append([]string{}, base...), "--dump-dom", url)
	domCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	out, derr := exec.CommandContext(domCtx, browser, domArgs...).Output()
	if derr != nil {
		res.Err = "headless dump-dom failed: " + derr.Error()
	}
	res.DOMTextLen = renderedTextLen(string(out))

	// 2) Best-effort screenshot (separate invocation; some Chrome builds don't emit
	// DOM + screenshot in one run reliably).
	if res.Screenshot != "" {
		shotArgs := append(append([]string{}, base...), "--screenshot="+res.Screenshot, url)
		shotCtx, cancel2 := context.WithTimeout(ctx, 25*time.Second)
		defer cancel2()
		if serr := exec.CommandContext(shotCtx, browser, shotArgs...).Run(); serr != nil {
			// Keep the DOM result; just note the screenshot didn't land.
			if res.Err == "" {
				res.Err = "screenshot failed: " + serr.Error()
			}
			res.Screenshot = ""
		} else if fi, e := os.Stat(res.Screenshot); e != nil || fi.Size() == 0 {
			res.Screenshot = ""
		}
	}
	return res
}

// renderedTextLen strips tags + collapses whitespace to approximate how much
// visible text the page rendered. A blank SPA (empty root) → ~0; a real page →
// hundreds+. Not exact, but the signal that separates a white screen from content.
func renderedTextLen(html string) int {
	if html == "" {
		return 0
	}
	// Drop script/style blocks so their contents don't count as "visible text".
	for _, tag := range []string{"script", "style", "noscript"} {
		re := regexp.MustCompile(`(?is)<` + tag + `\b.*?</` + tag + `>`)
		html = re.ReplaceAllString(html, " ")
	}
	text := htmlTagRE.ReplaceAllString(html, " ")
	text = wsRE.ReplaceAllString(text, " ")
	return len(strings.TrimSpace(text))
}
