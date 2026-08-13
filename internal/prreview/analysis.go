package prreview

import (
	"os/exec"
	"strings"
)

// CommitInfo is a short commit descriptor for the "new commits since analysis"
// list.
type CommitInfo struct {
	SHA     string `json:"sha"`     // short sha
	Subject string `json:"subject"` // commit message subject
}

// AnalysisStatus describes whether a repo's stored analysis is current relative
// to the mirror's HEAD, and (if not) which commits arrived since.
type AnalysisStatus struct {
	Analyzed    bool         `json:"analyzed"` // has the repo been analyzed at all?
	AnalyzedAt  string       `json:"analyzedAt,omitempty"`
	AnalyzedSHA string       `json:"analyzedSha,omitempty"`
	CurrentSHA  string       `json:"currentSha,omitempty"`
	UpToDate    bool         `json:"upToDate"`             // analyzedSHA == currentSHA
	NewCommits  []CommitInfo `json:"newCommits,omitempty"` // commits after analyzedSHA (up to a cap)
	CGNodes     int          `json:"cgNodes"`
	CGEdges     int          `json:"cgEdges"`
}

// AnalysisStatusFor compares a repo's recorded analysis against the mirror's
// current HEAD. gitDir is the bare mirror; branch is its default branch.
func (s *Service) AnalysisStatusFor(repoID, gitDir, branch string) (AnalysisStatus, error) {
	var st AnalysisStatus
	var analyzedSHA, analyzedAt string
	err := s.db.QueryRow(`
		SELECT head_sha, analyzed_at, cg_nodes, cg_edges
		  FROM pr_repo_analysis WHERE repo_id = ?`, repoID,
	).Scan(&analyzedSHA, &analyzedAt, &st.CGNodes, &st.CGEdges)
	if err != nil {
		// Never analyzed.
		st.Analyzed = false
		st.CurrentSHA = shortSHA(headSHA(gitDir, branch))
		return st, nil
	}
	st.Analyzed = true
	st.AnalyzedAt = analyzedAt
	st.AnalyzedSHA = shortSHA(analyzedSHA)

	current := headSHA(gitDir, branch)
	st.CurrentSHA = shortSHA(current)
	if current == "" || current == analyzedSHA {
		st.UpToDate = true
		return st, nil
	}

	// Commits reachable from current HEAD but not from the analyzed sha. If the
	// analyzed sha is no longer in history (force-push/rebase), git rev-list
	// errors — treat as "not up to date" with no listable commits.
	st.NewCommits = newCommitsSince(gitDir, analyzedSHA, current)
	st.UpToDate = false
	return st, nil
}

// newCommitsSince returns commits in (analyzedSHA, current], newest first, up to
// a cap. Empty if the range can't be computed (e.g. history was rewritten).
func newCommitsSince(gitDir, analyzedSHA, current string) []CommitInfo {
	out, err := exec.Command("git", "--git-dir", gitDir,
		"rev-list", "--max-count=25", "--format=%h%x1f%s",
		analyzedSHA+".."+current,
	).Output()
	if err != nil {
		return nil
	}
	var commits []CommitInfo
	for _, line := range strings.Split(string(out), "\n") {
		// rev-list --format prints a "commit <sha>" header line before each
		// formatted line; skip those and blanks.
		if line == "" || strings.HasPrefix(line, "commit ") {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		commits = append(commits, CommitInfo{SHA: parts[0], Subject: parts[1]})
	}
	return commits
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
