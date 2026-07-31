// Package repos manages the dashboard's repository list and the per-repo cache
// clones that back ephemeral, isolated project workspaces.
//
// Each added repo gets ONE bare `--mirror` cache clone under
// ~/.sandclaude/repos/<slug>.git. The cache is fetch-only — never worked in — so
// `git fetch` can never hit local-change/non-fast-forward problems. A project is
// then created by `git clone --local` from the cache into its own workspace: own
// .git + index (no shared-index locking like git worktrees), objects hardlinked
// (fast, disk-cheap), and N clones of one repo can run concurrently.
package repos

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackrothrock/sandclaude/internal/config"
)

// Repo is one entry in the repos list.
type Repo struct {
	ID            string `json:"id"`             // stable id derived from url|localPath
	Name          string `json:"name"`           // friendly label
	URL           string `json:"url"`            // remote URL (empty for a local-path source)
	LocalPath     string `json:"local_path"`     // local source path (empty for a URL source)
	IsPrivate     bool   `json:"is_private"`     // hint: clone via host git/gh auth
	CachePath     string `json:"cache_path"`     // ~/.sandclaude/repos/<slug>.git
	DefaultBranch string `json:"default_branch"` // best-effort, from the cache
	LastFetched   string `json:"last_fetched"`   // RFC3339
	AddedAt       string `json:"added_at"`       // RFC3339
}

type registry struct {
	Repos []Repo `json:"repos"`
}

func reposDir() string     { return filepath.Join(config.SandclaudeHome(), "repos") }
func registryPath() string { return filepath.Join(config.SandclaudeHome(), "repos.json") }

func readRegistry() (*registry, error) {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &registry{}, nil
		}
		return nil, fmt.Errorf("read repos.json: %w", err)
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("invalid repos.json: %w", err)
	}
	return &reg, nil
}

func writeRegistry(reg *registry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.SandclaudeHome(), 0700); err != nil {
		return err
	}
	return os.WriteFile(registryPath(), data, 0600)
}

// List returns the repos in the registry (never nil).
func List() ([]Repo, error) {
	reg, err := readRegistry()
	if err != nil {
		return nil, err
	}
	if reg.Repos == nil {
		return []Repo{}, nil
	}
	return reg.Repos, nil
}

// Get returns the repo with the given id.
func Get(id string) (*Repo, error) {
	reg, err := readRegistry()
	if err != nil {
		return nil, err
	}
	for i := range reg.Repos {
		if reg.Repos[i].ID == id {
			return &reg.Repos[i], nil
		}
	}
	return nil, fmt.Errorf("unknown repo id: %s", id)
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// idFor derives a stable id from the source (url or local path).
func idFor(source string) string {
	h := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", h[:6])
}

// slugFor makes a filesystem-safe, readable cache dir name from the repo name.
func slugFor(name, id string) string {
	base := strings.TrimSuffix(filepath.Base(name), ".git")
	base = slugRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-.")
	if base == "" {
		base = "repo"
	}
	return fmt.Sprintf("%s-%s.git", base, id)
}

// nameFromURL derives a friendly default name from a URL or local path.
func nameFromURL(source string) string {
	s := strings.TrimSuffix(source, ".git")
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "repo"
	}
	return s
}

// AddOptions describes a repo to add. Exactly one of URL or LocalPath is set.
type AddOptions struct {
	Name      string
	URL       string
	LocalPath string
	IsPrivate bool
}

// gitRunner runs a git command in dir and returns combined output. Overridable
// in tests. For private URLs the caller is responsible for ambient auth (host
// git/gh credential helpers) — repos does not handle credentials itself.
var gitRunner = func(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Add registers a repo and creates its bare --mirror cache clone. Returns the
// stored Repo. Errors if the source is already registered or the clone fails.
func Add(opts AddOptions) (*Repo, error) {
	source := opts.URL
	if source == "" {
		source = opts.LocalPath
	}
	if source == "" {
		return nil, fmt.Errorf("a url or local path is required")
	}

	reg, err := readRegistry()
	if err != nil {
		return nil, err
	}
	id := idFor(source)
	for i := range reg.Repos {
		if reg.Repos[i].ID == id {
			return nil, fmt.Errorf("repo already added: %s", source)
		}
	}

	name := opts.Name
	if name == "" {
		name = nameFromURL(source)
	}
	if err := os.MkdirAll(reposDir(), 0700); err != nil {
		return nil, err
	}
	cachePath := filepath.Join(reposDir(), slugFor(name, id))

	// Bare mirror: no working tree, so fetches never conflict; clones read from it.
	if _, err := gitRunner("", "clone", "--mirror", source, cachePath); err != nil {
		os.RemoveAll(cachePath) // don't leave a half-clone behind
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	repo := Repo{
		ID: id, Name: name, IsPrivate: opts.IsPrivate,
		CachePath: cachePath, DefaultBranch: detectDefaultBranch(cachePath),
		LastFetched: now, AddedAt: now,
	}
	if opts.URL != "" {
		repo.URL = opts.URL
	} else {
		repo.LocalPath = opts.LocalPath
	}
	reg.Repos = append(reg.Repos, repo)
	if err := writeRegistry(reg); err != nil {
		return nil, err
	}
	return &repo, nil
}

// Fetch refreshes a repo's cache mirror (git remote update). No working tree, so
// this can't fail on local changes. Needs ambient auth for private remotes.
func Fetch(id string) error {
	repo, err := Get(id)
	if err != nil {
		return err
	}
	if _, err := gitRunner(repo.CachePath, "remote", "update", "--prune"); err != nil {
		return err
	}
	// Update lastFetched.
	reg, err := readRegistry()
	if err != nil {
		return err
	}
	for i := range reg.Repos {
		if reg.Repos[i].ID == id {
			reg.Repos[i].LastFetched = time.Now().UTC().Format(time.RFC3339)
			if reg.Repos[i].DefaultBranch == "" {
				reg.Repos[i].DefaultBranch = detectDefaultBranch(repo.CachePath)
			}
			break
		}
	}
	return writeRegistry(reg)
}

// Remove deletes a repo from the registry and removes its cache mirror. Already
// spun-off projects have their own independent clones and are untouched.
func Remove(id string) error {
	reg, err := readRegistry()
	if err != nil {
		return err
	}
	out := reg.Repos[:0]
	var removed *Repo
	for i := range reg.Repos {
		if reg.Repos[i].ID == id {
			r := reg.Repos[i]
			removed = &r
			continue
		}
		out = append(out, reg.Repos[i])
	}
	if removed == nil {
		return fmt.Errorf("unknown repo id: %s", id)
	}
	reg.Repos = out
	if err := writeRegistry(reg); err != nil {
		return err
	}
	if removed.CachePath != "" {
		os.RemoveAll(removed.CachePath)
	}
	return nil
}

// detectDefaultBranch reads the mirror's HEAD to find its default branch;
// returns "" if it can't be determined.
func detectDefaultBranch(cachePath string) string {
	out, err := gitRunner(cachePath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
