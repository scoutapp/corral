// Package prreview implements Corral's PR Review feature: git forensics, PR
// block extraction, hotness ranking, and Claude-backed analysis over a repo's
// pull requests. It is the first tenant of the shared internal/store database.
//
// This package currently provides the data layer (types + store-backed
// queries) that the dashboard endpoints call. Forensics, block extraction, the
// GitHub (`gh`) client, and Claude (`claude` CLI) analysis land in later phases
// (see PR_REVIEW_INTEGRATION_PLAN.md §5).
package prreview

// Rows are keyed by Corral's existing repos.Repo.ID (a text sha), not an
// integer we own — the repo registry stays in repos.json. JSON tags mirror the
// TypeScript contracts in the dashboard's api/types.ts.

// FileStat is per-file git forensics for a repo.
type FileStat struct {
	ID           int64    `json:"id"`
	RepoID       string   `json:"repoId"`
	FilePath     string   `json:"filePath"`
	TotalCommits int      `json:"totalCommits"`
	FixCommits   int      `json:"fixCommits"`
	ChurnScore   *float64 `json:"churnScore,omitempty"`
	LastAnalyzed *string  `json:"lastAnalyzed,omitempty"`
}

// PR is a fetched pull request.
type PR struct {
	ID           int64   `json:"id"`
	RepoID       string  `json:"repoId"`
	Number       int     `json:"number"`
	Title        string  `json:"title,omitempty"`
	ShortSummary string  `json:"shortSummary,omitempty"`
	GithubURL    string  `json:"githubUrl,omitempty"`
	State        string  `json:"state,omitempty"`
	BaseSHA      string  `json:"baseSha,omitempty"`
	HeadSHA      string  `json:"headSha,omitempty"`
	FetchedAt    *string `json:"fetchedAt,omitempty"`
}

// Block is a logical, hotness-ranked chunk of a PR diff (a carousel item).
type Block struct {
	ID              int64    `json:"id"`
	PRID            int64    `json:"prId"`
	OrderIndex      int      `json:"orderIndex"`
	Priority        int      `json:"priority"`
	FilePath        string   `json:"filePath"`
	LineStart       int      `json:"lineStart"`
	LineEnd         int      `json:"lineEnd"`
	DiffHunk        string   `json:"diffHunk,omitempty"`
	Title           string   `json:"title,omitempty"`
	Explanation     string   `json:"explanation,omitempty"`
	CodebaseContext string   `json:"codebaseContext,omitempty"`
	HotnessScore    *float64 `json:"hotnessScore,omitempty"`
	IsTest          bool     `json:"isTest"`
}
