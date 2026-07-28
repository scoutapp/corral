package app

import (
	"strings"
	"time"
)

// ----------------------------------------------------------------------------
// Project activity — a coarse "working / waiting / off" signal.
//
// The proxy.log records every allowed connection with a timestamp, including the
// api.anthropic.com requests Claude Code makes. While Claude is actively working
// it streams completions and those requests cluster in bursts; when it finishes a
// turn and sits at the prompt waiting for you, they stop. So the RATE of recent
// api.anthropic.com hits distinguishes "working" from "waiting."
//
// Rate — not mere recency — matters: an idle Claude Code still makes sparse
// keep-alive/telemetry hits every several minutes, so "any recent hit == working"
// would false-positive. We require several hits inside a short window.
// ----------------------------------------------------------------------------

const (
	activityWindow    = 60 * time.Second // how far back to count hits
	activityWorkingN  = 3                 // >= this many hits in the window => working
	activityTailBytes = 64 * 1024         // proxy.log tail to scan (plenty for 60s)
	activityHost      = "api.anthropic.com"
)

// projectActivity classifies a project's current activity from proxy.log.
//   - "off":     container not up (nothing running)
//   - "working": container up AND >= activityWorkingN anthropic hits in the window
//   - "waiting": container up but few/no recent hits (Claude idle at the prompt)
//
// tmuxUp is accepted for future refinement (e.g. distinguishing a dead session)
// but a running container is the gate today. Returns the hit count for display.
func projectActivity(workspace string, containerUp, tmuxUp bool) (string, int) {
	if !containerUp {
		return "off", 0
	}

	logPath := logsDirForWorkspace(workspace) + "/proxy.log"
	// proxy.log is written by the in-container allowlist-proxy, whose clock is UTC,
	// so compare in UTC (see parseProxyLogTime).
	hits := countRecentAnthropicHits(logPath, time.Now().UTC(), activityWindow)

	if hits >= activityWorkingN {
		return "working", hits
	}
	return "waiting", hits
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
