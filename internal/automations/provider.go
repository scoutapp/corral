package automations

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// A capability action ("pr-approve", "pr-comment", …) is provider-agnostic: it
// describes WHAT to do, not HOW. The concrete HOW lives behind PRProvider, so
// GitHub (via gh) is one driver and GitLab/Gitea can be added later as new
// drivers without touching actions, hooks, flows, or the UI. This is what keeps
// "moving off GitHub" from being a rewrite.

// PRTarget identifies the pull request a capability acts on, in
// provider-neutral terms. The capability executor fills it from the run
// context; a driver maps it to its own CLI/API.
type PRTarget struct {
	OwnerName string // "owner/name" for GitHub; provider-specific project path elsewhere
	Number    int
	HeadSHA   string // needed for line comments
}

// PRProvider is the set of PR write capabilities a code host must implement.
// Methods return a human-readable error on failure (surfaced in the run log).
type PRProvider interface {
	Name() string // "github", "gitlab", …
	Approve(ctx context.Context, t PRTarget, body string) error
	RequestChanges(ctx context.Context, t PRTarget, body string) error
	Comment(ctx context.Context, t PRTarget, body string) error
	Merge(ctx context.Context, t PRTarget, method string) error
}

// Capability constants — the wire names stored in a capability action's spec.
const (
	CapApprove        = "pr-approve"
	CapComment        = "pr-comment"
	CapRequestChanges = "pr-request-changes"
	CapMerge          = "pr-merge"
)

// --- GitHub driver (gh CLI) ------------------------------------------------

// GitHubProvider implements PRProvider by shelling out to the `gh` CLI, mirroring
// the existing prreview PR actions (the gh commands are identical). It carries no
// state; the credential proxy injects the real token into gh's GitHub calls.
type GitHubProvider struct{}

func (GitHubProvider) Name() string { return "github" }

func (GitHubProvider) Approve(ctx context.Context, t PRTarget, body string) error {
	args := []string{"pr", "review", fmt.Sprint(t.Number), "--repo", t.OwnerName, "--approve"}
	if strings.TrimSpace(body) != "" {
		args = append(args, "--body", body)
	}
	return ghRun(ctx, args...)
}

func (GitHubProvider) RequestChanges(ctx context.Context, t PRTarget, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("a comment is required to request changes")
	}
	return ghRun(ctx, "pr", "review", fmt.Sprint(t.Number),
		"--repo", t.OwnerName, "--request-changes", "--body", body)
}

func (GitHubProvider) Comment(ctx context.Context, t PRTarget, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}
	return ghRun(ctx, "pr", "comment", fmt.Sprint(t.Number), "--repo", t.OwnerName, "--body", body)
}

func (GitHubProvider) Merge(ctx context.Context, t PRTarget, method string) error {
	flag := "--squash"
	switch method {
	case "merge":
		flag = "--merge"
	case "rebase":
		flag = "--rebase"
	case "squash", "":
		flag = "--squash"
	default:
		return fmt.Errorf("invalid merge method %q", method)
	}
	return ghRun(ctx, "pr", "merge", fmt.Sprint(t.Number), "--repo", t.OwnerName, flag)
}

// ghRun runs `gh` with a context (for timeout/cancel) and maps a non-zero exit
// to an error carrying gh's stderr, so the run log shows the real reason.
func ghRun(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return fmt.Errorf("gh CLI not found on PATH")
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
