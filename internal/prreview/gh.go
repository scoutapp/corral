package prreview

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

// ownerNameRe extracts "owner/name" from a GitHub URL (https or ssh, with or
// without .git). Mirrors the frontend ghOwnerName helper.
var ownerNameRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$`)

// OwnerName derives "owner/name" from a repo remote URL, or "" if it isn't a
// recognizable GitHub URL.
func OwnerName(url string) string {
	m := ownerNameRe.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// ghPRView is the subset of `gh pr view --json …` we store.
type ghPRView struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	URL         string `json:"url"`
	BaseRefOid  string `json:"baseRefOid"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"` // PR branch, for verify-launch
	Body        string `json:"body"`        // PR description (markdown)
}

// OpenPR is a lightweight entry from `gh pr list` — the repo's live open PRs,
// independent of what has been fetched/analyzed into the DB.
type OpenPR struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Author    string   `json:"author"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	Draft     bool     `json:"isDraft"`
	Review    string   `json:"reviewDecision"` // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | ""
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Labels    []string `json:"labels"`
}

// ghPRListItem mirrors one `gh pr list --json` row.
type ghPRListItem struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	Author         struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// ListOpenPRs returns the repo's open pull requests via `gh pr list` (reusing
// corral's GitHub token injection — no token handling here). This is a live
// GitHub read; it does not touch the DB. ownerName is "owner/name".
func ListOpenPRs(ownerName string, limit int) ([]OpenPR, error) {
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		return nil, fmt.Errorf("prreview: gh CLI not found on PATH")
	}
	if limit <= 0 {
		limit = 100
	}
	// All of these come back in the single list call — including reviewDecision
	// (approval state) and additions/deletions — so the overview needs no
	// per-PR fetches.
	out, err := exec.Command(ghBin, "pr", "list",
		"--repo", ownerName, "--state", "open", "--limit", fmt.Sprint(limit),
		"--json", "number,title,url,createdAt,updatedAt,isDraft,author,reviewDecision,additions,deletions,labels",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("prreview: gh pr list: %w", err)
	}
	var items []ghPRListItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("prreview: parse gh pr list: %w", err)
	}
	prs := make([]OpenPR, 0, len(items))
	for _, it := range items {
		labels := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}
		prs = append(prs, OpenPR{
			Number: it.Number, Title: it.Title, URL: it.URL,
			Author: it.Author.Login, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
			Draft: it.IsDraft, Review: it.ReviewDecision,
			Additions: it.Additions, Deletions: it.Deletions, Labels: labels,
		})
	}
	return prs, nil
}

// FetchPR pulls a PR's metadata and raw diff via the `gh` CLI (reusing corral's
// GitHub token injection — no token handling here) and upserts it into `prs`.
// ownerName is "owner/name". Returns the stored PR.
//
// Block extraction and AI summary are separate steps (later phases); this just
// lands the PR + its raw diff so the list populates and the diff is available.
func (s *Service) FetchPR(repoID, ownerName string, number int) (*PR, error) {
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		return nil, fmt.Errorf("prreview: gh CLI not found on PATH")
	}

	metaOut, err := exec.Command(ghBin, "pr", "view", fmt.Sprint(number),
		"--repo", ownerName,
		"--json", "number,title,state,url,baseRefOid,headRefOid,headRefName,body",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("prreview: gh pr view: %w", err)
	}
	var meta ghPRView
	if err := json.Unmarshal(metaOut, &meta); err != nil {
		return nil, fmt.Errorf("prreview: parse gh pr view: %w", err)
	}

	diffOut, err := exec.Command(ghBin, "pr", "diff", fmt.Sprint(number),
		"--repo", ownerName,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("prreview: gh pr diff: %w", err)
	}

	return s.upsertPR(repoID, meta, string(diffOut))
}

// upsertPR inserts or replaces a PR row (unique on repo_id + pr_number) and
// returns it. Existing blocks/analysis for a re-fetched PR are left in place;
// re-extraction (a later phase) will refresh them.
func (s *Service) upsertPR(repoID string, meta ghPRView, rawDiff string) (*PR, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO prs (repo_id, pr_number, title, body, github_url, state,
		                 base_sha, head_sha, head_ref, raw_diff, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, pr_number) DO UPDATE SET
		    title      = excluded.title,
		    body       = excluded.body,
		    github_url = excluded.github_url,
		    state      = excluded.state,
		    base_sha   = excluded.base_sha,
		    head_sha   = excluded.head_sha,
		    head_ref   = excluded.head_ref,
		    raw_diff   = excluded.raw_diff,
		    fetched_at = excluded.fetched_at
	`, repoID, meta.Number, meta.Title, meta.Body, meta.URL, meta.State,
		meta.BaseRefOid, meta.HeadRefOid, meta.HeadRefName, rawDiff, now)
	if err != nil {
		return nil, err
	}

	var p PR
	err = s.db.QueryRow(`
		SELECT id, repo_id, pr_number, COALESCE(title,''), COALESCE(body,''),
		       COALESCE(short_summary,''), COALESCE(github_url,''), COALESCE(state,''),
		       COALESCE(base_sha,''), COALESCE(head_sha,''), COALESCE(head_ref,''), fetched_at
		  FROM prs WHERE repo_id = ? AND pr_number = ?
	`, repoID, meta.Number).Scan(
		&p.ID, &p.RepoID, &p.Number, &p.Title, &p.Body, &p.ShortSummary,
		&p.GithubURL, &p.State, &p.BaseSHA, &p.HeadSHA, &p.HeadRef, &p.FetchedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
