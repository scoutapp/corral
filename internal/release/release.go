// Package release resolves the latest published corral release and compares
// it to the running build — shared by the CLI `update` command and the dashboard
// update-check. It deliberately avoids the GitHub API (no token, no rate limits):
// GitHub 302-redirects /releases/latest to /releases/tag/<tag>, so a single
// HEAD request reveals the newest tag anonymously — the same trick the
// curl|bash installer (scripts/install.sh) uses.
package release

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LatestTag returns the newest release tag (e.g. "v0.4.0") for the given release
// base URL (e.g. "https://github.com/owner/repo" or a self-hosted forge base) by
// following its /releases/latest redirect. The client does NOT follow redirects —
// it reads the Location header of the 302 and takes the last path segment. A
// short timeout keeps callers non-blocking; any error is returned so the caller
// can fall back to a cached/unknown value. The base is configurable
// (config.UpdateBaseURL) so a fork or a non-GitHub host can ship its own
// releases, as long as it uses GitHub-style /releases/latest + /releases/download
// paths (Gitea, GitLab, self-hosted GitHub, …).
func LatestTag(baseURL string) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/releases/latest"
	client := &http.Client{
		Timeout: 4 * time.Second,
		// Don't follow — we want the redirect's Location, not the target page.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		// No redirect (e.g. no releases yet, or a network intermediary). Not fatal.
		return "", fmt.Errorf("no redirect from %s (status %d)", url, resp.StatusCode)
	}
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if tag == "" || tag == "latest" {
		return "", fmt.Errorf("could not parse tag from redirect %q", loc)
	}
	// Guard against a redirect that doesn't land on a real release tag — e.g. a
	// login/marketing page (GitLab.com uses /-/releases, so /releases/latest
	// redirects to sign-in) would otherwise yield a nonsense "tag". Requiring a
	// version-shaped segment keeps us from reporting garbage as "latest".
	if !LooksLikeVersion(tag) {
		return "", fmt.Errorf("release source did not return a version tag (got %q from %q) — is this a GitHub-style /releases/latest endpoint?", tag, loc)
	}
	return tag, nil
}

// LooksLikeVersion reports whether s parses as a semver-ish tag (vX.Y.Z / X.Y.Z,
// ignoring any prerelease/build suffix). Exported so callers can validate a
// resolved tag before treating it as a real release.
func LooksLikeVersion(s string) bool {
	_, ok := parseSemver(s)
	return ok
}

// IsNewer reports whether release tag `latest` is strictly newer than `current`.
// Both are semver-ish tags ("v0.4.0" / "0.4.0"). A non-release current (e.g. the
// "dev" default from a plain `go build`) is treated as always-older so a dev
// build still sees "an update is available" rather than nothing. Unparseable
// latest → false (never nag on garbage).
func IsNewer(latest, current string) bool {
	lv, ok := parseSemver(latest)
	if !ok {
		return false
	}
	cv, ok := parseSemver(current)
	if !ok {
		return true // current is "dev"/unknown -> any real release is newer
	}
	for i := 0; i < 3; i++ {
		if lv[i] != cv[i] {
			return lv[i] > cv[i]
		}
	}
	return false
}

// parseSemver parses "vX.Y.Z" / "X.Y.Z" (ignoring any -prerelease/+build suffix)
// into [3]int. Returns ok=false for non-semver input.
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Drop a -prerelease or +build suffix.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
