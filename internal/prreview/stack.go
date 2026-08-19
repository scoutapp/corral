package prreview

import (
	"os/exec"
	"sort"
)

// Stacking is a PR's git-ancestry relationship to another PR in the same repo,
// derived from the actual commit graph (not GitHub, not file overlap). "Direct"
// means one branched off the exact tip of the other (base==head); "transitive"
// means one's head commit is an ancestor further back in the other's history.
type Stacking struct {
	PRID     int64  `json:"prId"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	HeadSHA  string `json:"headSha"`
	Relation string `json:"relation"` // "direct" | "transitive"
}

// StackResult reports how a PR sits in the repo's stack of open PRs.
type StackResult struct {
	// StackedOn: PRs this one is built ON — their head commit is an ancestor of
	// this PR's head (this PR's branch contains all of their commits). Ordered
	// nearest-first (direct parents before deeper ancestors).
	StackedOn []Stacking `json:"stackedOn"`
	// Dependents: PRs stacked ON this one — this PR's head is an ancestor of theirs.
	Dependents []Stacking `json:"dependents"`
}

// Stack computes the stacking relationships for a PR using the repo's bare git
// mirror (gitDir, from Repo.CachePath). For every other stored PR in the same
// repo it asks git whether one head sha is an ancestor of the other:
//
//	git --git-dir <mirror> merge-base --is-ancestor <maybe-ancestor> <descendant>
//
// exit 0 ⇒ ancestor. A PR whose head is an ancestor of this one is something this
// PR is stacked ON; the inverse makes this PR a dependency of that one. "direct"
// when the descendant's base sha equals the ancestor's head sha (branched off its
// tip), else "transitive". Best-effort: a PR with no shas, or a sha the mirror
// doesn't have (stale cache / not fetched), is skipped.
func (s *Service) Stack(prID int64, gitDir string) (StackResult, error) {
	var res StackResult
	if gitDir == "" {
		return res, nil
	}
	repoID, err := s.RepoIDForPR(prID)
	if err != nil {
		return res, err
	}
	self, err := s.prStackInfo(prID)
	if err != nil {
		return res, err
	}
	if self.head == "" {
		return res, nil // can't place a PR with no head sha
	}
	prs, err := s.PRs(repoID)
	if err != nil {
		return res, err
	}
	for _, p := range prs {
		if p.ID == prID || p.HeadSHA == "" {
			continue
		}
		// Is p an ancestor of self? → self is stacked on p.
		if isAncestor(gitDir, p.HeadSHA, self.head) {
			rel := "transitive"
			if self.base != "" && self.base == p.HeadSHA {
				rel = "direct"
			}
			res.StackedOn = append(res.StackedOn, Stacking{
				PRID: p.ID, Number: p.Number, Title: p.Title, HeadSHA: p.HeadSHA, Relation: rel,
			})
			continue
		}
		// Is self an ancestor of p? → p is stacked on self (a dependent).
		if isAncestor(gitDir, self.head, p.HeadSHA) {
			rel := "transitive"
			if p.BaseSHA != "" && p.BaseSHA == self.head {
				rel = "direct"
			}
			res.Dependents = append(res.Dependents, Stacking{
				PRID: p.ID, Number: p.Number, Title: p.Title, HeadSHA: p.HeadSHA, Relation: rel,
			})
		}
	}
	// Direct relationships first, then by PR number for stability.
	stackSort(res.StackedOn)
	stackSort(res.Dependents)
	return res, nil
}

type stackInfo struct{ head, base string }

func (s *Service) prStackInfo(prID int64) (stackInfo, error) {
	var si stackInfo
	err := s.db.QueryRow(
		`SELECT COALESCE(head_sha,''), COALESCE(base_sha,'') FROM prs WHERE id = ?`, prID,
	).Scan(&si.head, &si.base)
	return si, err
}

// isAncestor reports whether `ancestor` is an ancestor of `descendant` in the
// repo mirror. false on any git error (unknown sha, no mirror, etc.).
func isAncestor(gitDir, ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" || ancestor == descendant {
		return false
	}
	cmd := exec.Command("git", "--git-dir", gitDir, "merge-base", "--is-ancestor", ancestor, descendant)
	return cmd.Run() == nil // exit 0 ⇒ ancestor
}

func stackSort(xs []Stacking) {
	sort.SliceStable(xs, func(i, j int) bool {
		if (xs[i].Relation == "direct") != (xs[j].Relation == "direct") {
			return xs[i].Relation == "direct"
		}
		return xs[i].Number < xs[j].Number
	})
}
