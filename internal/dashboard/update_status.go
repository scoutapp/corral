package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/release"
)

// Update-availability check for the dashboard banner.
//
// The dashboard is a long-lived daemon and every open tab polls this endpoint
// (on load + every few hours), so we must NOT hit GitHub on every request. The
// latest tag is cached to ~/.sandclaude/update-check.json and only re-fetched
// when the cache is older than updateCheckTTL. GitHub is reached anonymously via
// the redirect trick (release.LatestTag) with a short timeout; any failure falls
// back to the cached value (or "unknown"), never blocking the UI.

const updateCheckTTL = 24 * time.Hour

type updateCache struct {
	Latest    string    `json:"latest"`
	Repo      string    `json:"repo"`
	CheckedAt time.Time `json:"checked_at"`
}

// updateCheckMu serializes the read-modify-write of the cache file across
// concurrent requests from multiple tabs.
var updateCheckMu sync.Mutex

func updateCachePath() string {
	return filepath.Join(config.CorralHome(), "update-check.json")
}

func readUpdateCache() updateCache {
	var c updateCache
	data, err := os.ReadFile(updateCachePath())
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

func writeUpdateCache(c updateCache) {
	if err := os.MkdirAll(config.CorralHome(), 0o700); err != nil {
		return
	}
	if data, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(updateCachePath(), data, 0o600)
	}
}

type updateStatusResp struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	Repo            string `json:"repo"`
	UpdateAvailable bool   `json:"update_available"`
	CheckedAt       string `json:"checked_at,omitempty"`
	// Unreachable is set when we have no usable latest tag AND the live check
	// failed (e.g. the repo is still private, or the network is down). The UI
	// shows a dismissible "update host unreachable" notice rather than nothing.
	Unreachable bool   `json:"unreachable,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleUpdateStatus reports whether a newer sandclaude release is available.
// It returns the cached latest tag, refreshing from GitHub only when the cache
// is stale (or the configured repo changed). Errors are non-fatal — we serve the
// last known value so the banner logic degrades to "no update" rather than an
// error.
func (d *dashboardServer) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	repo := config.UpdateRepo()
	baseURL := config.RepoToBaseURL(repo)
	current := config.Version

	updateCheckMu.Lock()
	cache := readUpdateCache()
	stale := cache.Latest == "" ||
		cache.Repo != repo ||
		cache.CheckedAt.IsZero() ||
		time.Since(cache.CheckedAt) > updateCheckTTL
	var checkErr error
	if stale {
		if latest, err := release.LatestTag(baseURL); err == nil && latest != "" {
			cache = updateCache{Latest: latest, Repo: repo, CheckedAt: time.Now()}
			writeUpdateCache(cache)
		} else {
			// Keep the (possibly empty) cached value — do not overwrite the
			// timestamp, so the next request retries rather than waiting a full
			// TTL. Remember the error so we can tell the UI "host unreachable".
			checkErr = err
			if checkErr == nil {
				checkErr = fmt.Errorf("empty latest tag from %s", repo)
			}
		}
	}
	updateCheckMu.Unlock()

	resp := updateStatusResp{
		Current: current,
		Latest:  cache.Latest,
		Repo:    repo,
	}
	if cache.Latest != "" {
		resp.UpdateAvailable = release.IsNewer(cache.Latest, current)
	}
	if !cache.CheckedAt.IsZero() {
		resp.CheckedAt = cache.CheckedAt.UTC().Format(time.RFC3339)
	}
	// Only report unreachable when we have NO usable tag to fall back on — a stale
	// cache that still yields a valid answer shouldn't nag about a transient blip.
	if cache.Latest == "" && checkErr != nil {
		resp.Unreachable = true
		resp.Error = checkErr.Error()
	}
	writeJSON(w, resp)
}
