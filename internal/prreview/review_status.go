package prreview

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReviewStatus is a lightweight snapshot of a PR's human-review state, shown on
// the PR page at load so you can see at a glance whether someone else has already
// reviewed/approved it and whether it has enough approvals to merge.
type ReviewStatus struct {
	// Decision is GitHub's rollup: "APPROVED", "CHANGES_REQUESTED",
	// "REVIEW_REQUIRED", or "" (no review policy / not determinable).
	Decision string `json:"decision"`
	// Approvals is the count of DISTINCT users whose latest review is APPROVED.
	Approvals int `json:"approvals"`
	// RequiredApprovals is the branch rule's required_approving_review_count, or 0
	// when it couldn't be read (no protection, or the token lacks access). When 0,
	// the UI shows just the approval count, not "X/N".
	RequiredApprovals int `json:"requiredApprovals"`
	// ChangesRequested is true if any current reviewer is blocking with changes.
	ChangesRequested bool `json:"changesRequested"`
	// Reviewers is the per-user latest review state (login → APPROVED /
	// CHANGES_REQUESTED / COMMENTED), newest-wins.
	Reviewers []Reviewer `json:"reviewers"`
	// Comments is the number of discussion comments on the PR (issue comments).
	Comments int `json:"comments"`
}

// Reviewer is one user's latest review verdict on the PR.
type Reviewer struct {
	Login string `json:"login"`
	State string `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED
}

// ReviewStatus fetches the PR's review rollup from GitHub in one `gh pr view`
// call, plus a best-effort branch-protection lookup for the required approval
// count. All of it is read-only and cheap; branch protection degrades to 0 (not
// an error) when the token can't read it, so the feature still works on repos
// where you're not an admin.
func (s *Service) ReviewStatus(prID int64, ownerName string) (*ReviewStatus, error) {
	ref, err := s.prRef(prID)
	if err != nil {
		return nil, err
	}
	out, err := runGh("pr", "view", fmt.Sprint(ref.Number), "--repo", ownerName,
		"--json", "reviewDecision,reviews,comments,baseRefName")
	if err != nil {
		return nil, err
	}
	var raw struct {
		ReviewDecision string `json:"reviewDecision"`
		BaseRefName    string `json:"baseRefName"`
		Reviews        []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State       string `json:"state"`
			SubmittedAt string `json:"submittedAt"`
		} `json:"reviews"`
		Comments []struct {
			ID string `json:"id"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("prreview: parse review status: %w", err)
	}

	// Collapse to the LATEST review per user (GitHub returns them chronologically),
	// ignoring pure COMMENTED/DISMISSED noise for the approval tally.
	latest := map[string]string{}
	for _, rv := range raw.Reviews {
		login := rv.Author.Login
		if login == "" {
			continue
		}
		switch rv.State {
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED":
			latest[login] = rv.State
		}
	}
	st := &ReviewStatus{
		Decision: raw.ReviewDecision,
		Comments: len(raw.Comments),
	}
	for login, state := range latest {
		if state == "DISMISSED" {
			continue
		}
		st.Reviewers = append(st.Reviewers, Reviewer{Login: login, State: state})
		if state == "APPROVED" {
			st.Approvals++
		}
		if state == "CHANGES_REQUESTED" {
			st.ChangesRequested = true
		}
	}

	// Required approvals from branch protection — best-effort. A 404/403 (no
	// protection, or no access) just leaves it 0.
	if raw.BaseRefName != "" {
		if pOut, perr := runGh("api", fmt.Sprintf("repos/%s/branches/%s/protection", ownerName, raw.BaseRefName),
			"--jq", ".required_pull_request_reviews.required_approving_review_count"); perr == nil {
			var n int
			if _, e := fmt.Sscanf(strings.TrimSpace(pOut), "%d", &n); e == nil {
				st.RequiredApprovals = n
			}
		}
	}
	return st, nil
}
