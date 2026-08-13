package prreview

import (
	"context"
	"encoding/json"
)

// preservingRunner is an aiRunner that returns previously-stored per-block
// analysis instead of calling Claude, keyed by the block's diff hunk. It lets a
// Rerank refresh hotness (from fresh repo churn/callgraph) WITHOUT losing the
// AI titles/explanations/edge-cases already generated — and without spending
// Claude calls. Blocks with no stored analysis fall through to placeholders.
type preservingRunner struct {
	byHunk  map[string]blockAnalysis
	summary string // the PR's existing short summary, returned for the summary prompt
}

// Run is invoked either by analyzeBlock (prompt embeds a diff hunk) or by
// summarizePR (prompt starts "Summarize this pull request"). For a block we
// return the stored analysis JSON; for the summary we return the stored summary
// verbatim — so a rerank preserves both without calling Claude. No match ⇒ empty
// object, letting analyzeBlock fall back to its placeholder.
func (p preservingRunner) Run(_ context.Context, prompt string) (string, error) {
	if indexOf(prompt, "Summarize this pull request") == 0 {
		return p.summary, nil
	}
	hunk := hunkFromPrompt(prompt)
	if a, ok := p.byHunk[hunk]; ok {
		b, _ := json.Marshal(a)
		return string(b), nil
	}
	return "{}", nil
}

// hunkFromPrompt extracts the diff hunk that blockPrompt embedded between the
// ```-fenced Diff block, so a stored analysis can be matched back to its block.
func hunkFromPrompt(prompt string) string {
	const marker = "Diff:\n```\n"
	i := indexOf(prompt, marker)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(marker):]
	j := indexOf(rest, "\n```")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Rerank recomputes a PR's block hotness against the repo's CURRENT churn +
// callgraph, preserving existing AI analysis (titles/explanations/edge-cases/
// summary) — no Claude calls. Use after the repo is (re)analyzed so a PR whose
// blocks were ranked against stale/absent data gets a fresh ranking cheaply.
func (s *Service) Rerank(ctx context.Context, prID int64) ([]Block, error) {
	snap, err := s.snapshotAnalysis(prID)
	if err != nil {
		return nil, err
	}
	var summary string
	_ = s.db.QueryRow(`SELECT COALESCE(short_summary,'') FROM prs WHERE id = ?`, prID).Scan(&summary)
	return s.ExtractBlocks(ctx, prID, preservingRunner{byHunk: snap, summary: summary})
}

// snapshotAnalysis reads the current blocks' AI fields keyed by diff hunk, so a
// re-extraction can restore them. The block prompt truncates hunks to 3000
// chars, so we key on the same truncation to guarantee a match.
func (s *Service) snapshotAnalysis(prID int64) (map[string]blockAnalysis, error) {
	rows, err := s.db.Query(`
		SELECT b.id, COALESCE(b.diff_hunk,''), COALESCE(b.title,''),
		       COALESCE(b.explanation,''), COALESCE(b.codebase_context,'')
		  FROM pr_blocks b WHERE b.pr_id = ?`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		id                     int64
		hunk, title, expl, ctx string
	}
	var rowsData []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.hunk, &r.title, &r.expl, &r.ctx); err != nil {
			return nil, err
		}
		rowsData = append(rowsData, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]blockAnalysis{}
	for _, r := range rowsData {
		// Only preserve blocks that actually carry AI text (skip placeholders).
		if r.expl == "" || r.expl == PlaceholderExplanation {
			continue
		}
		a := blockAnalysis{Title: r.title, Explanation: r.expl, CodebaseContext: r.ctx}
		// Edge cases for this block.
		ecRows, err := s.db.Query(
			`SELECT COALESCE(description,''), COALESCE(severity,'low') FROM pr_block_edge_cases WHERE block_id = ?`, r.id)
		if err == nil {
			for ecRows.Next() {
				var ec edgeCase
				if ecRows.Scan(&ec.Description, &ec.Severity) == nil {
					a.EdgeCases = append(a.EdgeCases, ec)
				}
			}
			ecRows.Close()
		}
		// Importance isn't stored directly; leave 0 so analyzeBlock normalizes it
		// to 3 (moderate). Hotness is recomputed from churn/callgraph regardless.
		out[truncateHunk(r.hunk)] = a
	}
	return out, nil
}

// truncateHunk mirrors blockPrompt's 3000-char cap so snapshot keys match the
// hunk embedded in the prompt at rerank time.
func truncateHunk(h string) string {
	if len(h) > 3000 {
		return h[:3000]
	}
	return h
}
