package prreview

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LinkedIssue is an issue a PR closes/links, with enough to show a description tab.
type LinkedIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"` // markdown
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`
	// Source records how the link was found: "closing" (GitHub's official
	// closingIssuesReferences) or "branch" (a number in the head branch name).
	Source string `json:"source,omitempty"`
}

// branchIssueRe pulls the first issue number out of a head branch name. Matches
// the common conventions: "issue-42-foo", "42-foo", "fix/issue-42", "gh-42".
// Requires a boundary so a bare version like "v2" or a sha suffix isn't mistaken
// for an issue number.
var branchIssueRe = regexp.MustCompile(`(?i)(?:^|[/_-])(?:issue[-_]?|gh[-_]?|#)?(\d{1,7})(?:$|[/_-])`)

// LinkedIssues returns the issues a PR closes, for the PR-view Issue tab(s).
//
// Resolution order (per the product decision):
//  1. GitHub's official closingIssuesReferences (the "Closes #N" link) via GraphQL.
//  2. If none, a number in the PR's head branch name (a convention-based guess).
//
// Each resolved issue is fetched (title + body) with `gh issue view`. ownerName
// is "owner/name". A PR that closes nothing returns an empty slice (no error).
func (s *Service) LinkedIssues(prID int64, ownerName string) ([]LinkedIssue, error) {
	var number int
	var headRef string
	err := s.db.QueryRow(
		`SELECT pr_number, COALESCE(head_ref,'') FROM prs WHERE id = ?`, prID,
	).Scan(&number, &headRef)
	if err != nil {
		return nil, err
	}

	nums, source := closingIssueNumbers(ownerName, number, headRef)
	issues := make([]LinkedIssue, 0, len(nums))
	for _, n := range nums {
		iss, err := fetchIssue(ownerName, n)
		if err != nil {
			// A referenced number that isn't a real issue (e.g. a branch-name
			// false positive, or a PR number) is skipped, not fatal.
			continue
		}
		iss.Source = source
		issues = append(issues, iss)
	}
	return issues, nil
}

// closingIssueNumbers resolves the issue numbers a PR closes, and how they were
// found. Tries the official GitHub link first; falls back to the branch name.
func closingIssueNumbers(ownerName string, prNumber int, headRef string) (nums []int, source string) {
	if refs := closingIssuesGraphQL(ownerName, prNumber); len(refs) > 0 {
		return refs, "closing"
	}
	if n, ok := issueNumberFromBranch(headRef); ok {
		return []int{n}, "branch"
	}
	return nil, ""
}

// closingIssuesGraphQL asks GitHub for the PR's closingIssuesReferences (the
// authoritative "this PR closes #N" links). Returns nil on any error — callers
// fall back to the branch-name heuristic.
func closingIssuesGraphQL(ownerName string, prNumber int) []int {
	owner, name, ok := splitOwnerName(ownerName)
	if !ok {
		return nil
	}
	const q = `query($owner:String!,$name:String!,$n:Int!){` +
		`repository(owner:$owner,name:$name){pullRequest(number:$n){` +
		`closingIssuesReferences(first:20){nodes{number}}}}}`
	out, err := runGh("api", "graphql",
		"-f", "query="+q,
		"-F", "owner="+owner,
		"-F", "name="+name,
		"-F", "n="+strconv.Itoa(prNumber),
	)
	if err != nil {
		return nil
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ClosingIssuesReferences struct {
						Nodes []struct {
							Number int `json:"number"`
						} `json:"nodes"`
					} `json:"closingIssuesReferences"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &resp) != nil {
		return nil
	}
	var nums []int
	for _, node := range resp.Data.Repository.PullRequest.ClosingIssuesReferences.Nodes {
		if node.Number > 0 {
			nums = append(nums, node.Number)
		}
	}
	return nums
}

// issueNumberFromBranch extracts an issue number from a head branch name, or
// (0,false) if none is present.
func issueNumberFromBranch(headRef string) (int, bool) {
	m := branchIssueRe.FindStringSubmatch(headRef)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// fetchIssue pulls one issue's number/title/body/url/state via `gh issue view`.
func fetchIssue(ownerName string, number int) (LinkedIssue, error) {
	out, err := runGh("issue", "view", strconv.Itoa(number),
		"--repo", ownerName,
		"--json", "number,title,body,url,state",
	)
	if err != nil {
		return LinkedIssue{}, err
	}
	var iss LinkedIssue
	if err := json.Unmarshal([]byte(out), &iss); err != nil {
		return LinkedIssue{}, fmt.Errorf("parse gh issue view: %w", err)
	}
	return iss, nil
}

// splitOwnerName splits "owner/name" into its parts.
func splitOwnerName(ownerName string) (owner, name string, ok bool) {
	parts := strings.SplitN(ownerName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
