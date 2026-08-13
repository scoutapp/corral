package prreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RiskVerdict is Claude's PR-level risk assessment (mirrors the reference
// analyze_pr_risk output). Stored as JSON in prs.ai_analysis.
type RiskVerdict struct {
	Meat        string       `json:"meat"`        // core change, 1 sentence
	BugImpact   string       `json:"bugImpact"`   // what breaks if a bug slips in
	FileHealth  []FileHealth `json:"fileHealth"`  // per-file risk notes
	FixHistory  string       `json:"fixHistory"`  // follow-up-fix pattern?
	OverallRisk string       `json:"overallRisk"` // high|medium|low
	RiskSummary string       `json:"riskSummary"` // <=12 words
}

type FileHealth struct {
	File    string `json:"file"`
	Risk    string `json:"risk"` // high|medium|low
	Insight string `json:"insight"`
}

// AnalyzeRisk asks Claude for a PR-level risk verdict, using the PR's blocks and
// file forensics as context, and stores the result in prs.ai_analysis. Returns
// the verdict. Requires a non-nil ai runner; without one it returns an error so
// the caller can surface "claude unavailable" rather than a misleading verdict.
func (s *Service) AnalyzeRisk(ctx context.Context, prID int64, ai aiRunner) (*RiskVerdict, error) {
	if ai == nil {
		return nil, fmt.Errorf("prreview: risk analysis needs the claude CLI (not found)")
	}

	var repoID, title, rawDiff string
	if err := s.db.QueryRow(
		`SELECT repo_id, COALESCE(title,''), COALESCE(raw_diff,'') FROM prs WHERE id = ?`, prID,
	).Scan(&repoID, &title, &rawDiff); err != nil {
		return nil, err
	}

	blocks, err := s.Blocks(prID)
	if err != nil {
		return nil, err
	}
	// File forensics for the files this PR touches.
	touched := map[string]bool{}
	for _, b := range blocks {
		touched[b.FilePath] = true
	}
	stats, _ := s.Forensics(repoID)
	var healthLines, blockLines []string
	for _, st := range stats {
		if touched[st.FilePath] {
			churn := 0.0
			if st.ChurnScore != nil {
				churn = *st.ChurnScore
			}
			healthLines = append(healthLines, fmt.Sprintf(
				"- %s: churn=%.2f, fix=%d/%d commits",
				st.FilePath, churn, st.FixCommits, st.TotalCommits))
		}
	}
	for _, b := range blocks {
		if b.Explanation != "" {
			blockLines = append(blockLines, fmt.Sprintf(
				"- %s:%d-%d: %s", b.FilePath, b.LineStart, b.LineEnd, b.Explanation))
		}
	}

	diff := rawDiff
	if len(diff) > 6000 {
		diff = diff[:6000]
	}
	prompt := riskPrompt(title, strings.Join(healthLines, "\n"),
		strings.Join(blockLines, "\n"), diff)

	out, err := ai.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	var v RiskVerdict
	if err := json.Unmarshal([]byte(stripFences(out)), &v); err != nil {
		return nil, fmt.Errorf("prreview: parse risk verdict: %w", err)
	}

	raw, _ := json.Marshal(v)
	if _, err := s.db.Exec(`UPDATE prs SET ai_analysis = ? WHERE id = ?`, string(raw), prID); err != nil {
		return nil, err
	}
	return &v, nil
}

// StoredRisk returns the last stored risk verdict for a PR, or nil if none.
func (s *Service) StoredRisk(prID int64) (*RiskVerdict, error) {
	var raw string
	err := s.db.QueryRow(`SELECT COALESCE(ai_analysis,'') FROM prs WHERE id = ?`, prID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var v RiskVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, nil // stale/incompatible blob → treat as absent
	}
	return &v, nil
}

func riskPrompt(title, fileHealth, blockContext, diff string) string {
	if fileHealth == "" {
		fileHealth = "No forensics data available."
	}
	if blockContext == "" {
		blockContext = "No block analysis available."
	}
	return "You are a senior engineer reviewing a pull request for risk and impact.\n\n" +
		"PR Title: " + title + "\n\n" +
		"File change history (churn = commits per day since first touch):\n" + fileHealth + "\n\n" +
		"Block-level analysis:\n" + blockContext + "\n\n" +
		"Diff (truncated):\n```diff\n" + diff + "\n```\n\n" +
		"Return a JSON object with exactly these keys:\n" +
		`{"meat": "1 tight sentence: the core change", ` +
		`"bugImpact": "1 sentence: what breaks and how badly if a bug slips in", ` +
		`"fileHealth": [{"file": "path", "risk": "high|medium|low", "insight": "<=10 words"}], ` +
		`"fixHistory": "1 sentence: do these files show a pattern of follow-up fixes?", ` +
		`"overallRisk": "high|medium|low", ` +
		`"riskSummary": "<=12 words: the risk verdict"}` + "\n\n" +
		"Only return valid JSON, no markdown fences."
}
