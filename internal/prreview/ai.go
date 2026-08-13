package prreview

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// aiRunner runs a single prompt through Claude and returns the raw text reply.
// The dashboard supplies a claude-CLI-backed implementation; tests supply a
// fake. A nil runner (no claude available) makes the AI steps fall back to
// deterministic placeholders so block extraction still works.
type aiRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeCLI runs the host `claude` CLI one-shot, reusing corral's model of
// shelling out to the user's claude (no API keys in-process). Bin is an
// absolute path the dashboard resolves via resolveClaudeBin.
type claudeCLI struct{ Bin string }

// NewClaudeRunner returns an aiRunner backed by the `claude` CLI at bin, or nil
// if bin is empty (callers treat nil as "AI unavailable → use placeholders").
func NewClaudeRunner(bin string) aiRunner {
	if bin == "" {
		return nil
	}
	return claudeCLI{Bin: bin}
}

// Run invokes `claude -p <prompt> --output-format text`. A per-call timeout
// keeps a hung CLI from blocking a request indefinitely.
func (c claudeCLI) Run(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// --output-format text prints just the assistant's final text (no stream
	// framing), which is what we parse. The prompt is passed as an argv value,
	// never spliced into a shell.
	cmd := exec.CommandContext(ctx, c.Bin, "-p", prompt, "--output-format", "text")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// fenceRe strips a leading/trailing markdown code fence Claude sometimes wraps
// JSON in despite instructions.
var fenceRe = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n?(.*?)\\n?```$")

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

// blockAnalysis is Claude's per-block verdict (mirrors the reference prompt).
type blockAnalysis struct {
	Title           string     `json:"title"`
	Explanation     string     `json:"explanation"`
	CodebaseContext string     `json:"codebase_context"`
	EdgeCases       []edgeCase `json:"edge_cases"`
	Importance      int        `json:"importance"`
	// RiskScore (0-100) is the AI's INFORMED judgment of how likely THIS change
	// is to cause issues, weighing the file heuristics (churn/fixes/authors/
	// callgraph) against how well-guarded/tested the actual added code is. It
	// becomes the block's hotness when AI analysis is present, so a well-guarded
	// change in a churny file can rank LOWER than the raw heuristics suggest.
	RiskScore int `json:"risk_score"`
}

// fileHeuristic bundles the mechanical signals for a file, passed into the block
// prompt so the AI can reason about risk with the same evidence the heuristic
// ranking uses.
type fileHeuristic struct {
	Churn         float64
	TotalCommits  int
	FixCommits    int
	AuthorCount   int
	InDegree      int
	DaysSinceEdit int // -1 if unknown
}

type edgeCase struct {
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// analyzeBlock asks Claude to analyze one diff block, giving it the file's
// mechanical heuristics so it can judge risk with the same evidence the
// heuristic ranking uses. On any failure (no runner, CLI error, unparseable
// JSON) it returns a deterministic placeholder, so extraction always produces a
// usable block.
func analyzeBlock(ctx context.Context, ai aiRunner, diffHunk, filePath, prTitle string, h fileHeuristic) blockAnalysis {
	fallback := placeholderAnalysis(filePath)
	if ai == nil {
		return fallback
	}
	prompt := blockPrompt(diffHunk, filePath, prTitle, h)
	out, err := ai.Run(ctx, prompt)
	if err != nil {
		return fallback
	}
	var a blockAnalysis
	if err := json.Unmarshal([]byte(stripFences(out)), &a); err != nil {
		return fallback
	}
	if a.Title == "" {
		a.Title = fallback.Title
	}
	if a.Importance < 1 || a.Importance > 5 {
		a.Importance = 3
	}
	if a.RiskScore < 0 {
		a.RiskScore = 0
	}
	if a.RiskScore > 100 {
		a.RiskScore = 100
	}
	return a
}

// PlaceholderExplanation is the block explanation used when no AI analysis is
// available (the View step, or claude unavailable). The frontend matches it to
// show "new file"/"not analyzed"; Rerank matches it to skip preserving
// placeholder text. Keep the three in sync.
const PlaceholderExplanation = "This block modifies the file. Claude analysis is unavailable."

func placeholderAnalysis(filePath string) blockAnalysis {
	base := filePath
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return blockAnalysis{
		Title:           "Changes in " + base,
		Explanation:     PlaceholderExplanation,
		CodebaseContext: "Context unavailable without the claude CLI.",
		Importance:      3,
	}
}

func blockPrompt(diffHunk, filePath, prTitle string, h fileHeuristic) string {
	if len(diffHunk) > 3000 {
		diffHunk = diffHunk[:3000]
	}
	return "Analyze this code diff block from a pull request for RISK — how likely " +
		"is this specific change to introduce a bug or cause problems.\n\n" +
		"PR Title: " + prTitle + "\n" +
		"File: " + filePath + "\n\n" +
		"File heuristics (historical signals — evidence, not verdict):\n" +
		heuristicLines(h) + "\n" +
		"Diff:\n```\n" + diffHunk + "\n```\n\n" +
		"Judge the risk of THIS change. The heuristics show the file's history, " +
		"but weigh them against the ACTUAL code: a change to a high-churn / " +
		"heavily-fixed / widely-called file can still be LOW risk if the added " +
		"code is simple, well-guarded (null/permission/error checks), covered by " +
		"tests, or purely additive; conversely a small change to a 'calm' file can " +
		"be HIGH risk if it touches core invariants, auth, money, or data. Don't " +
		"just echo the heuristics.\n\n" +
		"Respond with a JSON object containing:\n" +
		`- "title": <=8 word title of what this block does` + "\n" +
		`- "explanation": 1 sentence: what changed and the practical effect` + "\n" +
		`- "codebase_context": <=10 words on how this fits the broader codebase` + "\n" +
		`- "edge_cases": array of {"description": str, "severity": "low"|"medium"|"high"}` + "\n" +
		`- "importance": integer 1-5 (1=critical security/auth/payments/data, ` +
		`2=significant error-handling/API/DB, 3=moderate refactor/config, ` +
		`4=minor tests/docs, 5=trivial comments/formatting)` + "\n" +
		`- "risk_score": integer 0-100 — your informed likelihood that THIS change ` +
		`causes an issue (0=trivially safe, 100=very likely to break something). ` +
		`This is the primary output; it should reflect the code, not just the ` +
		`file's churn.` + "\n\n" +
		"Return only valid JSON, no markdown fences."
}

// heuristicLines renders the file signals for the prompt.
func heuristicLines(h fileHeuristic) string {
	stale := "unknown"
	if h.DaysSinceEdit >= 0 {
		stale = fmt.Sprintf("%d days ago", h.DaysSinceEdit)
	}
	return fmt.Sprintf(
		"- churn score: %.2f commits/day\n"+
			"- fix commits: %d of %d total (files with many fix commits have been buggy)\n"+
			"- distinct authors: %d\n"+
			"- callgraph in-degree: %d (how many places call into this file — blast radius)\n"+
			"- last edited: %s",
		h.Churn, h.FixCommits, h.TotalCommits, h.AuthorCount, h.InDegree, stale)
}

// summarizePR asks Claude for a <=100-char PR summary from the PR title and its
// block titles. Falls back to the (truncated) PR title on any failure.
func summarizePR(ctx context.Context, ai aiRunner, prTitle string, blockTitles []string, fallback string) string {
	if ai == nil {
		return truncate100(fallback)
	}
	prompt := "Summarize this pull request in under 100 characters.\n" +
		"PR Title: " + prTitle + "\n" +
		"Key changes: " + strings.Join(blockTitles, ", ") + "\n" +
		"Return only the summary string, no quotes or extra text."
	out, err := ai.Run(ctx, prompt)
	if err != nil {
		return truncate100(fallback)
	}
	s := strings.TrimSpace(out)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return truncate100(fallback)
	}
	return truncate100(s)
}

func truncate100(s string) string {
	r := []rune(s)
	if len(r) <= 100 {
		return s
	}
	return string(r[:99]) + "…"
}
