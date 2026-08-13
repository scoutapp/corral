package prreview

import (
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
