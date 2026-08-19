package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DefaultUpdateRepo is the GitHub owner/name self-updates are pulled from unless
// overridden in global settings. A fork that ships its own releases can point
// here instead (see GlobalSettings.UpdateRepo).
const DefaultUpdateRepo = "scoutapp/corral"

// GlobalSettings holds cross-project, host-level preferences stored in
// ~/.corral/global-settings.json. It is intentionally small — individual
// concerns (ssh keys, credentials) keep their own files; this is the catch-all
// for scalar host settings that don't warrant a dedicated file.
type GlobalSettings struct {
	// UpdateRepo is the GitHub "owner/name" that `corral update` and the
	// dashboard update-check resolve releases from. Empty = DefaultUpdateRepo.
	UpdateRepo string `json:"update_repo,omitempty"`

	// AutomationsToolsEnabled gates whether automations actions are exposed as
	// callable tools to the host Claude. OFF by default: a user must explicitly
	// opt in, since a tool lets Claude trigger real side effects (gh approve,
	// bash, webhooks). This is the user-permission part of the tool adapter.
	AutomationsToolsEnabled bool `json:"automations_tools_enabled,omitempty"`

	// LogRetentionDays / LogMaxRows bound the application log (app_logs). Zero
	// means "use the built-in default" (30 days / 100k rows). The retention job
	// prunes on dashboard start + hourly.
	LogRetentionDays int `json:"log_retention_days,omitempty"`
	LogMaxRows       int `json:"log_max_rows,omitempty"`

	// ApiWritesEnabled gates MUTATING API calls (POST/PUT/DELETE) made with the
	// API token — i.e. the `corral api` CLI and the host Claude skill. Reads are
	// always allowed, but a write needs explicit opt-in since it can start
	// projects, create issues, run flows. The browser dashboard (its own session
	// token) is never gated — this is only for programmatic clients.
	//
	// TRI-STATE: nil = the user has never chosen (the dashboard prompts them, like
	// the chat capability), true = allowed, false = explicitly denied. nil is
	// treated as OFF for enforcement (ApiWritesAllowed), so the safe posture holds
	// until they decide — but unlike a plain bool, nil is distinguishable from a
	// deliberate "off", which is what lets the UI know to ask.
	ApiWritesEnabled *bool `json:"api_writes_enabled,omitempty"`

	// DindDefault is the default Docker-in-Docker state for NEW projects that don't
	// specify one at creation. A project needs DinD (which runs the container
	// --privileged) to use Docker inside the sandbox; without it, `docker` fails
	// deep in overlayfs with a cap_sys_admin error. So this DEFAULTS TO ON — a
	// fresh sandbox "just works" for Docker — while a user who wants the tighter,
	// unprivileged box can flip it off (globally here, or per-project).
	//
	// *bool so nil (never set — the fresh-install case) is distinguishable and
	// resolves to the true default; false means the user deliberately chose off.
	DindDefault *bool `json:"dind_default,omitempty"`

	// MergeStrategy is the default method the PR merge UI pre-selects and the
	// "rebase & merge in sandbox" flow tells Claude to use: "squash" | "merge" |
	// "rebase". Empty = the built-in default ("squash"). It's only a default — the
	// per-merge UI can still pick a different method.
	MergeStrategy string `json:"merge_strategy,omitempty"`

	// MergeAutoTeardown controls whether a "merge in sandbox" job auto-removes its
	// one-shot sandbox once the PR is merged. DEFAULTS TO ON — a merge sandbox is a
	// fire-and-forget worker, so cleaning it up automatically is the expected
	// behavior; a user who wants to inspect how conflicts were resolved before the
	// box disappears can flip it off (then tear down manually).
	//
	// *bool so nil (never set) resolves to the ON default, distinguishable from a
	// deliberate off.
	MergeAutoTeardown *bool `json:"merge_auto_teardown,omitempty"`

	// MergeMode is which merge EXECUTION mode the PR merge split-button makes
	// primary: "host" (host Claude with Bash against a throwaway host checkout —
	// fast, not sandboxed), "sandbox" (one-shot container, Claude rebases/merges,
	// torn down), or "plain" (`gh pr merge`, today's behavior). Empty = the
	// built-in default ("host") — lead with speed; the ▾ dropdown always lets you
	// pick any mode per-merge, and this only sets the default primary action.
	MergeMode string `json:"merge_mode,omitempty"`
}

// ValidMergeMode reports whether s is one of the three merge execution modes.
func ValidMergeMode(s string) bool {
	return s == "sandbox" || s == "host" || s == "plain"
}

// MergeModeOrDefault returns the effective default merge mode, falling back to
// "host" (the fast path) when unset or invalid.
func (gs *GlobalSettings) MergeModeOrDefault() string {
	if gs != nil && ValidMergeMode(gs.MergeMode) {
		return gs.MergeMode
	}
	return "host"
}

// ValidMergeStrategy reports whether s is one of the three GitHub merge methods.
func ValidMergeStrategy(s string) bool {
	return s == "squash" || s == "merge" || s == "rebase"
}

// MergeStrategyOrDefault returns the effective default merge method, falling back
// to "squash" when unset or invalid.
func (gs *GlobalSettings) MergeStrategyOrDefault() string {
	if gs != nil && ValidMergeStrategy(gs.MergeStrategy) {
		return gs.MergeStrategy
	}
	return "squash"
}

// MergeAutoTeardownOn reports the effective auto-teardown default for merge
// sandboxes. Nil (never set) → ON, so a merge worker cleans itself up by default.
func (gs *GlobalSettings) MergeAutoTeardownOn() bool {
	return gs == nil || gs.MergeAutoTeardown == nil || *gs.MergeAutoTeardown
}

// DindDefaultOn reports the effective default DinD state for new projects. Nil
// (never set) → ON, so a fresh install gets a working-Docker sandbox by default.
func (gs *GlobalSettings) DindDefaultOn() bool {
	return gs == nil || gs.DindDefault == nil || *gs.DindDefault
}

// ApiWritesAllowed reports the effective enforcement value: true only when the
// user has explicitly enabled writes. nil (never chosen) and false both block.
func (gs *GlobalSettings) ApiWritesAllowed() bool {
	return gs != nil && gs.ApiWritesEnabled != nil && *gs.ApiWritesEnabled
}

// ApiWritesConfigured reports whether the user has made a choice (true or false).
// nil means "never chosen" → the dashboard should prompt.
func (gs *GlobalSettings) ApiWritesConfigured() bool {
	return gs != nil && gs.ApiWritesEnabled != nil
}

// GlobalSettingsPath is ~/.corral/global-settings.json.
func GlobalSettingsPath() string {
	return filepath.Join(CorralHome(), "global-settings.json")
}

// ReadGlobalSettings loads global settings; a missing or unparseable file
// yields zero-value settings (never an error) so a fresh install just uses
// defaults.
func ReadGlobalSettings() *GlobalSettings {
	gs := &GlobalSettings{}
	data, err := os.ReadFile(GlobalSettingsPath())
	if err != nil {
		return gs
	}
	_ = json.Unmarshal(data, gs) // tolerate a corrupt file → defaults
	return gs
}

// WriteGlobalSettings persists global settings (0600, dir 0700).
func WriteGlobalSettings(gs *GlobalSettings) error {
	if gs == nil {
		gs = &GlobalSettings{}
	}
	data, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(CorralHome(), 0700); err != nil {
		return err
	}
	return os.WriteFile(GlobalSettingsPath(), data, 0600)
}

// UpdateRepoOrDefault returns the effective, normalized update SOURCE for
// display: the configured value (normalized) or DefaultUpdateRepo when
// unset/malformed. The value is either a GitHub "owner/name" (the common case)
// or a full base URL to a release host that uses GitHub-style paths (Gitea,
// GitLab, self-hosted GitHub, …). A bad edit falls back to the default rather
// than pointing updates at nonsense.
func (gs *GlobalSettings) UpdateRepoOrDefault() string {
	if norm, ok := NormalizeRepo(gs.UpdateRepo); ok {
		return norm
	}
	return DefaultUpdateRepo
}

// UpdateBaseURL returns the effective release base URL — the directory under
// which the /releases/latest redirect and /releases/download/<tag>/<asset>
// artifacts live. A short "owner/name" maps to https://github.com/owner/name;
// a full URL source is used as-is (trailing slash trimmed).
func (gs *GlobalSettings) UpdateBaseURL() string {
	return RepoToBaseURL(gs.UpdateRepoOrDefault())
}

// NormalizeRepo cleans a user-entered update source into either:
//   - a bare GitHub "owner/name" (when the input is the short form or a
//     github.com URL), or
//   - a full base URL "https://host/owner/repo" (for any other forge/host),
//
// trimming whitespace, trailing slashes and a ".git" suffix. Returns ok=false
// only when the input can't be understood as either shape.
func NormalizeRepo(s string) (string, bool) {
	r := strings.TrimSpace(s)
	if r == "" {
		return "", false
	}
	r = strings.TrimRight(r, "/")
	r = strings.TrimSuffix(r, ".git")

	// Full URL form (http/https): a non-github host is kept as a base URL;
	// a github.com URL is collapsed back to the short owner/name form.
	if strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "https://") {
		rest := r
		rest = strings.TrimPrefix(rest, "http://")
		rest = strings.TrimPrefix(rest, "https://")
		host := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			host = rest[:i]
		}
		if host == "github.com" {
			ownerName := strings.TrimPrefix(rest, "github.com/")
			if isOwnerName(ownerName) {
				return ownerName, true
			}
			return "", false
		}
		// Non-github URL: require at least host + one path segment so it looks
		// like a repo base, not a bare hostname.
		if strings.Contains(rest, "/") {
			return r, true
		}
		return "", false
	}

	// Bare "github.com/owner/repo" without a scheme → short form.
	r = strings.TrimPrefix(r, "github.com/")
	if isOwnerName(r) {
		return r, true
	}
	return "", false
}

// isOwnerName reports whether s is a two-segment, non-empty "owner/name".
func isOwnerName(s string) bool {
	parts := strings.Split(s, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// RepoToBaseURL maps a normalized source to its release base URL: a short
// "owner/name" → https://github.com/owner/name; a full URL is returned as-is.
func RepoToBaseURL(source string) string {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return strings.TrimRight(source, "/")
	}
	return "https://github.com/" + source
}

// UpdateRepo is the convenience top-level accessor for the effective source
// (display form) without reading settings first.
func UpdateRepo() string {
	return ReadGlobalSettings().UpdateRepoOrDefault()
}

// UpdateBaseURL is the convenience top-level accessor for the effective release
// base URL.
func UpdateBaseURL() string {
	return ReadGlobalSettings().UpdateBaseURL()
}
