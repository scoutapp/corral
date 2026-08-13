package prreview

import (
	"fmt"
	"strings"
)

// ChatContext builds a system-style preamble for a PR (optionally scoped to a
// block) that the dashboard prepends to the first chat turn: PR summary, the
// current block's diff + explanation, and the repo's hottest files. Mirrors the
// reference _build_system_prompt. blockID <= 0 means PR-level (no block).
//
// Returned as a plain string the caller injects into the prompt, since corral's
// chat transport passes context via the prompt, not a separate system flag.
func (s *Service) ChatContext(prID, blockID int64) (string, error) {
	var repoID, title, summary string
	var number int
	err := s.db.QueryRow(`
		SELECT repo_id, COALESCE(title,''), COALESCE(short_summary,''), pr_number
		  FROM prs WHERE id = ?`, prID).Scan(&repoID, &title, &summary, &number)
	if err != nil {
		return "", err
	}

	var parts []string
	parts = append(parts,
		fmt.Sprintf("You are a code review assistant helping with PR #%d: %s.", number, title))
	if summary == "" {
		summary = title
	}
	parts = append(parts, "PR Summary: "+summary)

	if blockID > 0 {
		var filePath, bTitle, explanation, diffHunk string
		var lineStart, lineEnd int
		err := s.db.QueryRow(`
			SELECT file_path, COALESCE(title,''), COALESCE(explanation,''),
			       COALESCE(diff_hunk,''), line_start, line_end
			  FROM pr_blocks WHERE id = ? AND pr_id = ?`, blockID, prID,
		).Scan(&filePath, &bTitle, &explanation, &diffHunk, &lineStart, &lineEnd)
		if err == nil {
			label := bTitle
			if label == "" {
				label = filePath
			}
			parts = append(parts, fmt.Sprintf(
				"\nCurrently viewing block: %s (lines %d-%d)", label, lineStart, lineEnd))
			if explanation != "" {
				parts = append(parts, "What it does: "+explanation)
			}
			if diffHunk != "" {
				if len(diffHunk) > 1500 {
					diffHunk = diffHunk[:1500]
				}
				parts = append(parts, "\nDiff:\n```\n"+diffHunk+"\n```")
			}
		}
	}

	// Top hot files for repo-level context.
	if stats, err := s.Forensics(repoID); err == nil && len(stats) > 0 {
		n := 3
		if len(stats) < n {
			n = len(stats)
		}
		var hot []string
		for _, st := range stats[:n] {
			hot = append(hot, fmt.Sprintf("%s (%d fix commits)", st.FilePath, st.FixCommits))
		}
		parts = append(parts, "\nHottest files in repo: "+strings.Join(hot, ", "))
	}

	parts = append(parts,
		"\nFocus on potential edge cases, missed test scenarios, and knowledge transfer. Be concise.")
	return strings.Join(parts, "\n"), nil
}
