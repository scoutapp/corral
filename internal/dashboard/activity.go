package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ----------------------------------------------------------------------------
// Project activity — a coarse "working / waiting / off" signal that drives the
// landing page's chime + toast (they fire on a working→waiting edge).
//
// The naive signal — "any recent api.anthropic.com hit == working" — over-fires:
// it can't tell a completion the USER asked for from one Claude Code runs on its
// OWN (generating a conversation title, a summary). Real proxy.log data shows
// those background calls as short 3–8 hit bursts that look identical, host-wise,
// to a real turn. The only thing that distinguishes them is the request PATH
// (/v1/messages vs Claude Code's internal endpoints) — which the allowlist-proxy
// can't see (it CONNECT-tunnels HTTPS as an opaque byte-splice; TLS terminates at
// mitmweb). So we use a HYBRID:
//
//   - PRECISE (preferred): when api.anthropic.com is being MITM'd, mitmweb has the
//     decrypted flows. Count POST api.anthropic.com/v1/messages flows in the
//     window — the real user-facing completion calls, nothing else.
//   - FALLBACK: when it isn't MITM'd (or mitmweb is unreachable), fall back to
//     proxy.log host-counting, but SUPPRESS short isolated bursts so a single
//     auto-completion doesn't notify — require a sustained burst.
// ----------------------------------------------------------------------------

const (
	activityWindow    = 60 * time.Second // how far back to count hits
	activityWorkingN  = 3                // >= this many /v1/messages hits => working (precise path)
	activityTailBytes = 64 * 1024        // proxy.log tail to scan (plenty for 60s)
	activityHost      = "api.anthropic.com"
	activityMsgPath   = "/v1/messages"

	// Fallback (host-only) threshold. Higher than the precise one: without the
	// path we can't exclude auto-completions, so we demand a bigger sustained
	// burst — a lone 3–8 hit auto-completion won't reach it, a real streaming turn
	// will. Derived from real logs (auto-completions cluster at 3–8 hits/60s).
	activityFallbackN = 9
)

// projectActivity classifies a project's current activity.
//   - "off":     container not up
//   - "working": container up AND enough recent user-facing completion activity
//   - "waiting": container up but idle at the prompt
//
// mitmWebPort is the project's mitmweb port (0 if mitm isn't up); when nonzero and
// api.anthropic.com is actually being decrypted, we use the precise /v1/messages
// count. Otherwise we fall back to the proxy.log heuristic. Returns the hit count
// for display.
func projectActivity(workspace string, containerUp, tmuxUp bool, mitmWebPort int) (string, int) {
	if !containerUp {
		return "off", 0
	}

	// Precise path: count POST /v1/messages flows from mitmweb.
	if mitmWebPort > 0 {
		if hits, ok := countRecentMessageFlows(mitmWebPort, time.Now(), activityWindow); ok {
			if hits >= activityWorkingN {
				return "working", hits
			}
			return "waiting", hits
		}
		// ok==false → api.anthropic.com isn't in the flows (not MITM'd) or mitmweb
		// was unreachable; fall through to the proxy.log heuristic.
	}

	// Fallback: proxy.log host-count with burst suppression.
	logPath := logsDirForWorkspace(workspace) + "/proxy.log"
	hits := countRecentAnthropicHits(logPath, time.Now().UTC(), activityWindow)
	if hits >= activityFallbackN {
		return "working", hits
	}
	return "waiting", hits
}

// mitmFlow is the subset of a mitmweb /flows entry we read.
type mitmFlow struct {
	Request struct {
		Method     string  `json:"method"`
		Host       string  `json:"host"`
		PrettyHost string  `json:"pretty_host"`
		Path       string  `json:"path"`
		TSStart    float64 `json:"timestamp_start"`
	} `json:"request"`
}

// countRecentMessageFlows counts POST api.anthropic.com/v1/messages flows whose
// request started within `window` of now. The bool is false when the data is
// unusable for this purpose — mitmweb unreachable, or NO api.anthropic.com flow
// present at all (so we can't tell "idle" from "not being decrypted") — signaling
// the caller to fall back. A present anthropic host with zero recent /v1/messages
// is a legitimate "waiting" and returns (0, true).
func countRecentMessageFlows(webPort int, now time.Time, window time.Duration) (int, bool) {
	upstream := fmt.Sprintf("http://127.0.0.1:%d/flows", webPort)
	req, err := http.NewRequest(http.MethodGet, upstream, nil)
	if err != nil {
		return 0, false
	}
	// mitmweb rejects any Host that isn't a bare loopback IP (rebinding guard).
	req.Host = fmt.Sprintf("127.0.0.1:%d", webPort)
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false
	}

	var flows []mitmFlow
	if err := json.NewDecoder(resp.Body).Decode(&flows); err != nil {
		return 0, false
	}

	cutoff := now.Add(-window).Unix()
	count := 0
	sawAnthropic := false
	for _, f := range flows {
		host := f.Request.PrettyHost
		if host == "" {
			host = f.Request.Host
		}
		if !hostIsAnthropicAPI(host) {
			continue
		}
		sawAnthropic = true
		if f.Request.Method != http.MethodPost || f.Request.Path != activityMsgPath {
			continue // token-counting, model list, and Claude Code's own calls are excluded
		}
		if int64(f.Request.TSStart) >= cutoff {
			count++
		}
	}
	if !sawAnthropic {
		// No anthropic flows at all → it isn't being MITM'd here; the flow data
		// can't answer the question. Fall back.
		return 0, false
	}
	return count, true
}

// hostIsAnthropicAPI matches api.anthropic.com exactly (ignoring a :port), NOT
// statsig/other subdomains — those are background telemetry, not completions.
func hostIsAnthropicAPI(host string) bool {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.EqualFold(host, activityHost)
}

// countRecentAnthropicHits scans the tail of proxy.log and counts lines mentioning
// api.anthropic.com whose timestamp is within `window` of `now`. proxy.log lines
// look like "2026/07/28 01:44:43 ALLOWED  api.anthropic.com:443" (local time) or
// the passthrough form "2026/07/28 00:58:21   - api.anthropic.com".
func countRecentAnthropicHits(logPath string, now time.Time, window time.Duration) int {
	lines, _, err := readTailLines(logPath, activityTailBytes, 2000)
	if err != nil {
		return 0
	}

	cutoff := now.Add(-window)
	count := 0
	for _, line := range lines {
		if !strings.Contains(line, activityHost) {
			continue
		}
		ts, ok := parseProxyLogTime(line)
		if !ok {
			continue
		}
		if ts.After(cutoff) {
			count++
		}
	}
	return count
}

// parseProxyLogTime extracts the leading "2006/01/02 15:04:05" timestamp from a
// proxy.log line. The allowlist-proxy that writes proxy.log runs INSIDE the
// sandbox container, whose clock is UTC — while the dashboard daemon runs on the
// host in local time. So the timestamp is parsed as UTC and callers compare
// against time.Now().UTC(); parsing as local would skew the window by the host's
// UTC offset and wreck the working/waiting classification.
func parseProxyLogTime(line string) (time.Time, bool) {
	// The timestamp is the first 19 chars: "2006/01/02 15:04:05".
	if len(line) < 19 {
		return time.Time{}, false
	}
	ts, err := time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
