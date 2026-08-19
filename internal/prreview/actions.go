package prreview

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// PRRef is the GitHub coordinates + head sha of a stored PR, needed to run `gh`
// actions against it.
type PRRef struct {
	Number  int
	HeadSHA string
}

// RepoIDForPR returns the Corral Repo.ID a stored PR belongs to.
func (s *Service) RepoIDForPR(prID int64) (string, error) {
	var repoID string
	err := s.db.QueryRow(`SELECT repo_id FROM prs WHERE id = ?`, prID).Scan(&repoID)
	return repoID, err
}

// prRef loads a stored PR's number + head sha. ownerName is supplied by the
// caller (derived from the repo URL), so this only reads the PR row.
func (s *Service) prRef(prID int64) (PRRef, error) {
	var ref PRRef
	err := s.db.QueryRow(
		`SELECT pr_number, COALESCE(head_sha,'') FROM prs WHERE id = ?`, prID,
	).Scan(&ref.Number, &ref.HeadSHA)
	return ref, err
}

// PRHookContext returns the stored PR fields the automations engine needs to
// build a run context (number, github url, head sha, title). Exported so the
// dashboard can fire event hooks after a PR write action without reaching into
// prreview internals.
func (s *Service) PRHookContext(prID int64) (number int, url, headSHA, title string, err error) {
	err = s.db.QueryRow(
		`SELECT pr_number, COALESCE(github_url,''), COALESCE(head_sha,''), COALESCE(title,'')
		   FROM prs WHERE id = ?`, prID,
	).Scan(&number, &url, &headSHA, &title)
	return
}

// gh runs the `gh` CLI with args and returns combined output, mapping a failure
// to an error that includes gh's stderr (so the UI can surface the reason).
func runGh(args ...string) (string, error) {
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("gh CLI not found on PATH")
	}
	out, err := exec.Command(ghBin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return string(out), nil
}

// Approve submits an approving review (optional body). gh pr review --approve.
func (s *Service) Approve(prID int64, ownerName, body string) error {
	ref, err := s.prRef(prID)
	if err != nil {
		return err
	}
	args := []string{"pr", "review", fmt.Sprint(ref.Number), "--repo", ownerName, "--approve"}
	if strings.TrimSpace(body) != "" {
		args = append(args, "--body", body)
	}
	_, err = runGh(args...)
	return err
}

// RequestChanges submits a changes-requested review. A body is required by
// GitHub for this review type.
func (s *Service) RequestChanges(prID int64, ownerName, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("a comment is required to request changes")
	}
	ref, err := s.prRef(prID)
	if err != nil {
		return err
	}
	_, err = runGh("pr", "review", fmt.Sprint(ref.Number),
		"--repo", ownerName, "--request-changes", "--body", body)
	return err
}

// Comment posts a general (non-line) PR comment. gh pr comment.
func (s *Service) Comment(prID int64, ownerName, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}
	ref, err := s.prRef(prID)
	if err != nil {
		return err
	}
	_, err = runGh("pr", "comment", fmt.Sprint(ref.Number),
		"--repo", ownerName, "--body", body)
	return err
}

// Merge merges the PR with the given method ("squash"|"merge"|"rebase").
func (s *Service) Merge(prID int64, ownerName, method string) error {
	ref, err := s.prRef(prID)
	if err != nil {
		return err
	}
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
	_, err = runGh("pr", "merge", fmt.Sprint(ref.Number), "--repo", ownerName, flag)
	return err
}

// MergeInfo carries the stored PR fields the "merge in sandbox" flow needs to
// build its launch prompt (number/title/url + the PR branch to check out).
type MergeInfo struct {
	Number  int
	Title   string
	URL     string
	HeadRef string
}

// PRMergeInfo loads a PR's number/title/url/head-ref for the merge-in-sandbox
// launch prompt.
func (s *Service) PRMergeInfo(prID int64) (MergeInfo, error) {
	var mi MergeInfo
	err := s.db.QueryRow(
		`SELECT pr_number, COALESCE(title,''), COALESCE(github_url,''), COALESCE(head_ref,'')
		   FROM prs WHERE id = ?`, prID,
	).Scan(&mi.Number, &mi.Title, &mi.URL, &mi.HeadRef)
	return mi, err
}

// MergeState is a live snapshot of a PR's merge status, read straight from
// GitHub (not the local cache) — used to poll a "merge in sandbox" job to
// completion.
type MergeState struct {
	State  string `json:"state"` // OPEN | MERGED | CLOSED
	Merged bool   `json:"-"`     // convenience: State == MERGED
	Number int    `json:"number"`
}

// PRMergeState reads a PR's current merge status from GitHub via
// `gh pr view <n> --json state,number`. ownerName is the "owner/name" repo slug.
func (s *Service) PRMergeState(prID int64, ownerName string) (MergeState, error) {
	ref, err := s.prRef(prID)
	if err != nil {
		return MergeState{}, err
	}
	out, err := runGh("pr", "view", fmt.Sprint(ref.Number), "--repo", ownerName, "--json", "state,number")
	if err != nil {
		return MergeState{}, err
	}
	var ms MergeState
	if err := json.Unmarshal([]byte(out), &ms); err != nil {
		return MergeState{}, err
	}
	ms.Merged = strings.EqualFold(ms.State, "MERGED")
	return ms, nil
}

// LineComment posts a review comment anchored to a diff line. side is "RIGHT"
// (new file) or "LEFT" (old). Uses gh api pulls/<n>/comments with the PR's head
// commit, matching the reference implementation.
func (s *Service) LineComment(prID int64, ownerName, body, path string, line int, side string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}
	ref, err := s.prRef(prID)
	if err != nil {
		return err
	}
	if ref.HeadSHA == "" {
		return fmt.Errorf("PR has no head SHA — re-view the PR first")
	}
	if side != "LEFT" && side != "RIGHT" {
		side = "RIGHT"
	}
	_, err = runGh("api",
		fmt.Sprintf("repos/%s/pulls/%d/comments", ownerName, ref.Number),
		"--method", "POST",
		"--field", "body="+body,
		"--field", "commit_id="+ref.HeadSHA,
		"--field", "path="+path,
		"--field", fmt.Sprintf("line=%d", line),
		"--field", "side="+side,
	)
	return err
}
