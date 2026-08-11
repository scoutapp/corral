package config

import (
	"fmt"
	"runtime"
)

// Build metadata. Overridden at release time via -ldflags -X, e.g.
//   go build -ldflags "-X github.com/scoutapp/corral/internal/config.Version=v1.2.3 \
//     -X github.com/scoutapp/corral/internal/config.Commit=abc1234 \
//     -X github.com/scoutapp/corral/internal/config.Date=2026-07-28T00:00:00Z"
// GoReleaser sets all three. A plain `go build` leaves the dev defaults.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// VersionString is the one-line human-readable build identity, e.g.
// "sandclaude v1.2.3 (abc1234, 2026-07-28T00:00:00Z) darwin/arm64".
func VersionString() string {
	return fmt.Sprintf("sandclaude %s (%s, %s) %s/%s",
		Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
