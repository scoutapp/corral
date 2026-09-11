package prreview

import (
	"context"
	"fmt"
	"strings"
)

// promptPRReview is the editable catalog key for the full-review prompt (mirrors
// automations.PromptPRReview). The resolver applies the repo/global override.
const promptPRReview = "pr.review"

// FullReview is the result of the "Full Review" workflow: a markdown review a
// human can paste onto the PR, plus the when for staleness.
type FullReview struct {
	Markdown   string `json:"markdown"`
	ReviewedAt string `json:"reviewedAt,omitempty"`
}

// RunFullReview is the review WORKFLOW: it gathers the prior analysis (risk
// verdict, file heuristics, per-block AI findings) plus the PR description and
// diff, feeds them into the editable pr.review prompt, runs the (host, read-only)
// ai runner, and stores the markdown result (latest-wins). It does NOT itself
// run the risk/heuristics analysis — the caller ensures those exist first (they
// live in separate endpoints); RunFullReview consumes whatever is stored, and
// degrades gracefully (missing pieces become "not available" notes) so a review
// is still produced.
func (s *Service) RunFullReview(ctx context.Context, prID int64, ai aiRunner) (*FullReview, error) {
	if ai == nil {
		return nil, fmt.Errorf("prreview: full review needs the claude CLI (not found)")
	}

	var repoID, title, body, rawDiff string
	if err := s.db.QueryRow(
		`SELECT repo_id, COALESCE(title,''), COALESCE(body,''), COALESCE(raw_diff,'') FROM prs WHERE id = ?`, prID,
	).Scan(&repoID, &title, &body, &rawDiff); err != nil {
		return nil, err
	}
	number, _, _, _, _ := s.PRHookContext(prID)

	// Prior risk verdict (stored JSON), rendered as a compact readable block.
	riskText := "No risk analysis available."
	if v, _ := s.StoredRisk(prID); v != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "Overall: %s — %s\n", v.OverallRisk, v.RiskSummary)
		if v.Meat != "" {
			fmt.Fprintf(&b, "Core change: %s\n", v.Meat)
		}
		if v.BugImpact != "" {
			fmt.Fprintf(&b, "Bug impact: %s\n", v.BugImpact)
		}
		if v.FixHistory != "" {
			fmt.Fprintf(&b, "Fix history: %s\n", v.FixHistory)
		}
		for _, fh := range v.FileHealth {
			fmt.Fprintf(&b, "- %s [%s]: %s\n", fh.File, fh.Risk, fh.Insight)
		}
		riskText = strings.TrimSpace(b.String())
	}

	blocks, _ := s.Blocks(prID)

	// File heuristics (forensics) for the files this PR touches.
	touched := map[string]bool{}
	for _, bl := range blocks {
		touched[bl.FilePath] = true
	}
	var heurLines []string
	if stats, _ := s.Forensics(repoID); len(stats) > 0 {
		for _, st := range stats {
			if !touched[st.FilePath] {
				continue
			}
			churn := 0.0
			if st.ChurnScore != nil {
				churn = *st.ChurnScore
			}
			heurLines = append(heurLines, fmt.Sprintf(
				"- %s: churn=%.2f, fix=%d/%d commits", st.FilePath, churn, st.FixCommits, st.TotalCommits))
		}
	}
	heuristics := strings.Join(heurLines, "\n")
	if heuristics == "" {
		heuristics = "No file heuristics available."
	}

	// Per-block AI findings (title + explanation of each analyzed hunk).
	var findLines []string
	for _, bl := range blocks {
		if bl.Explanation == "" && bl.Title == "" {
			continue
		}
		loc := bl.FilePath
		if bl.LineStart > 0 {
			loc = fmt.Sprintf("%s:%d-%d", bl.FilePath, bl.LineStart, bl.LineEnd)
		}
		findLines = append(findLines, fmt.Sprintf("- %s — %s: %s", loc, bl.Title, bl.Explanation))
	}
	findings := strings.Join(findLines, "\n")
	if findings == "" {
		findings = "No per-block findings available."
	}

	if strings.TrimSpace(body) == "" {
		body = "(no PR description)"
	}
	// Cap the diff so the prompt stays within budget (the findings/risk already
	// distill the important parts; the diff is corroborating context).
	diff := rawDiff
	if len(diff) > 12000 {
		diff = diff[:12000] + "\n… (diff truncated)"
	}

	prompt := s.reviewPrompt(repoID, number, title, body, riskText, heuristics, findings, diff)

	out, err := ai.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	md := strings.TrimSpace(stripFences(out))
	if md == "" {
		return nil, fmt.Errorf("prreview: full review returned empty")
	}

	if _, err := s.db.Exec(
		`UPDATE prs SET full_review = ?, full_review_at = datetime('now') WHERE id = ?`, md, prID,
	); err != nil {
		return nil, err
	}
	return s.StoredReview(prID)
}

// reviewPrompt renders the editable pr.review prompt with the assembled context,
// falling back to a self-contained default when no override is configured.
func (s *Service) reviewPrompt(repoID string, number int, title, description, risk, heuristics, findings, diff string) string {
	slots := map[string]string{
		"repo":           repoID,
		"pr_number":      fmt.Sprintf("%d", number),
		"pr_title":       title,
		"pr_description": description,
		"risk":           risk,
		"heuristics":     heuristics,
		"findings":       findings,
		"diff":           diff,
	}
	fallback := fmt.Sprintf(
		"You are doing a thorough review of pull request #%d (%q) in %s.\n\n"+
			"PR description:\n%s\n\nRisk verdict:\n%s\n\nFile heuristics / hot spots:\n%s\n\n"+
			"Per-block AI findings:\n%s\n\nThe diff:\n```\n%s\n```\n\n"+
			"Don't treat the heuristics as binary — evaluate the ENTIRE PR. Look for commonly-missed issues: "+
			"unhandled edge cases, concurrency, idempotency, at-least-once / at-most-once semantics and how they "+
			"impact this PR. Decide whether the PR is purely additive or subtractive/a fix; for a fix, find and "+
			"justify the likely ROOT CAUSE rather than the symptom. Note performance concerns for hot-path changes. "+
			"Write clear markdown a human can paste onto the PR: a short summary, then specific findings.",
		number, title, repoID, description, risk, heuristics, findings, diff)
	return s.renderPrompt(promptPRReview, repoID, slots, fallback)
}

// StoredReview returns the last stored full-review markdown for a PR, or nil.
func (s *Service) StoredReview(prID int64) (*FullReview, error) {
	var md, at string
	err := s.db.QueryRow(
		`SELECT COALESCE(full_review,''), COALESCE(full_review_at,'') FROM prs WHERE id = ?`, prID,
	).Scan(&md, &at)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(md) == "" {
		return nil, nil
	}
	return &FullReview{Markdown: md, ReviewedAt: at}, nil
}
