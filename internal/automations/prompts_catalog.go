package automations

// The prompt catalog is the registry of every prompt Corral sends to Claude.
// Each entry carries a stable key, a friendly name, a "where it's used" callout,
// the built-in default template (the exact text the code ships), and the slot
// names the code fills. This is what makes every prompt editable with a
// three-level override (built-in → global → repo, repo wins) instead of being a
// hard-coded string buried in a handler.
//
// Overrides are stored as ordinary claude_prompt actions named "prompt:<key>"
// (scope global or repo), so no new schema is needed. A call site renders the
// effective template with its slot values via RenderPrompt.
//
// Slots use the same {{name}} syntax as everything else. If a user deletes a
// slot from their override, that data simply isn't injected — the prompt still
// works, it just omits that context.

// PromptDef describes one catalog entry.
type PromptDef struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	UsedWhen string  `json:"usedWhen"` // the "where is this used" callout
	Default string   `json:"default"`  // built-in template
	Slots   []string `json:"slots"`    // slot names the code fills
}

// Prompt keys — the stable identifiers call sites use.
const (
	PromptProjectStart = "project.start"
	PromptProjectIssue = "project.issue"
	PromptPRVerify     = "pr.verify"
	PromptAnalyzeBlock = "analyze.block"
	PromptAnalyzeSummary = "analyze.summary"
	PromptRisk         = "pr.risk"
	PromptChatPreamble = "chat.preamble"
	PromptDraftIssue   = "draft.issue"
	PromptDraftPrompt  = "draft.prompt"
)

// sshPushGuidance is appended to the project-start / issue defaults so Claude
// knows to push over the SSH remote — the HTTPS remote won't authenticate in the
// sandbox (no token), but the project's scoped ssh-agent holds the key. The
// {{ssh_remote}} slot is filled with git@github.com:<owner/name>.git when a key
// is configured, and blanked (with the guidance omitted) when not.
const sshPushGuidance = " When you push, use the SSH remote ({{ssh_remote}}) — the scoped ssh-agent has the key; the HTTPS remote won't authenticate here."

// PromptCatalog returns every editable prompt with its built-in default, in
// display order. This is the single source of truth for the carousel UI and the
// call sites (which look up their default here rather than inlining it).
func PromptCatalog() []PromptDef {
	return []PromptDef{
		{
			Key:  PromptProjectStart,
			Name: "Project start",
			UsedWhen: "Typed into Claude when a plain sandbox project launches (New project, or Verify-in-sandbox without a preset).",
			Default: "You're working in a sandboxed checkout of {{repo}} on branch {{branch}}. " +
				"Explore the codebase, then help with the task at hand." + sshPushGuidance,
			Slots: []string{"repo", "branch", "ssh_remote"},
		},
		{
			Key:  PromptProjectIssue,
			Name: "Project start (from an issue)",
			UsedWhen: "Typed into Claude when a project is created from a GitHub issue.",
			Default: "Work on {{repo}} issue #{{number}}: {{title}}. The full description is in ISSUE.md at the workspace root. You're on branch {{branch}}." + sshPushGuidance,
			Slots:   []string{"repo", "number", "title", "branch", "ssh_remote"},
		},
		{
			Key:  PromptPRVerify,
			Name: "Verify PR in sandbox",
			UsedWhen: "Auto-submitted to Claude when you click ▶ Verify in sandbox on a PR (using the built-in prompt).",
			Default: `Verify PR #{{pr_number}} ("{{pr_title}}") works. You're on its branch. ` +
				"Explore the change, run the relevant tests or the app, and report whether it behaves correctly " +
				"and any issues you find. The PR is {{pr_url}}.",
			Slots: []string{"pr_number", "pr_title", "pr_url"},
		},
		{
			Key:  PromptAnalyzeBlock,
			Name: "Analyze — block risk",
			UsedWhen: "Sent once per code block when you click Analyze with AI on a PR. Must return the JSON described at the end.",
			Default: "Analyze this code diff block from a pull request for RISK — how likely " +
				"is this specific change to introduce a bug or cause problems.\n\n" +
				"PR Title: {{pr_title}}\n" +
				"File: {{file}}\n\n" +
				"File heuristics (historical signals — evidence, not verdict):\n" +
				"{{heuristics}}\n" +
				"Diff:\n```\n{{diff}}\n```\n\n" +
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
				"Return only valid JSON, no markdown fences.",
			Slots: []string{"pr_title", "file", "heuristics", "diff"},
		},
		{
			Key:  PromptAnalyzeSummary,
			Name: "Analyze — PR summary",
			UsedWhen: "Sent during Analyze with AI to produce the short (<100 char) PR summary.",
			Default: "Summarize this pull request in under 100 characters.\n" +
				"PR Title: {{pr_title}}\n" +
				"Key changes: {{key_changes}}\n" +
				"Return only the summary string, no quotes or extra text.",
			Slots: []string{"pr_title", "key_changes"},
		},
		{
			Key:  PromptRisk,
			Name: "PR risk assessment",
			UsedWhen: "Sent when the PR-level Risk assessment runs. Must return the JSON described at the end.",
			Default: "You are a senior engineer reviewing a pull request for risk and impact.\n\n" +
				"PR Title: {{title}}\n\n" +
				"File change history (churn = commits per day since first touch):\n{{file_health}}\n\n" +
				"Block-level analysis:\n{{block_context}}\n\n" +
				"Diff (truncated):\n```diff\n{{diff}}\n```\n\n" +
				"Return a JSON object with exactly these keys:\n" +
				`{"meat": "1 tight sentence: the core change", ` +
				`"bugImpact": "1 sentence: what breaks and how badly if a bug slips in", ` +
				`"fileHealth": [{"file": "path", "risk": "high|medium|low", "insight": "<=10 words"}], ` +
				`"fixHistory": "1 sentence: do these files show a pattern of follow-up fixes?", ` +
				`"overallRisk": "high|medium|low", ` +
				`"riskSummary": "<=12 words: the risk verdict"}` + "\n\n" +
				"Only return valid JSON, no markdown fences.",
			Slots: []string{"title", "file_health", "block_context", "diff"},
		},
		{
			Key:  PromptChatPreamble,
			Name: "Ask Claude — context preamble",
			UsedWhen: "Prepended to the first message when you Ask Claude about a PR or block. The {{context}} slot holds the PR/block summary Corral assembles.",
			Default: "You are a code review assistant helping with this pull request.\n{{context}}\n" +
				"Focus on potential edge cases, missed test scenarios, and knowledge transfer. Be concise.",
			Slots: []string{"context"},
		},
		{
			Key:  PromptDraftIssue,
			Name: "Draft an issue with AI — instructions",
			UsedWhen: "The instruction given to the host Claude when you click Draft with AI on a new issue (before it researches the repo).",
			Default: "You are drafting a GitHub issue for this repository. " +
				"Research the codebase (read relevant files, grep, explore the structure) so the issue is concrete and grounded in the actual code. " +
				"The user's request:\n\n{{description}}\n\n" +
				"Investigate, then briefly summarize what you found and what the issue should cover. Do not write the final issue yet.",
			Slots: []string{"description"},
		},
		{
			Key:  PromptDraftPrompt,
			Name: "Build a prompt with AI — instructions",
			UsedWhen: "The instruction given to the host Claude when you click Build with AI on the project-start prompt.",
			Default: "You are writing a project-start prompt: the first instruction given to Claude Code when it " +
				"opens a sandboxed checkout of a repository to work on it. " +
				"The prompt is a reusable TEMPLATE; you may include {{repo}}, {{branch}}, {{pr_number}}, {{pr_title}}, {{pr_url}} placeholders.\n\n" +
				"The user wants a prompt that: {{description}}\n\n" +
				"Research this codebase so the prompt is concrete, then briefly summarize what you found. Do not write the final prompt yet.",
			Slots: []string{"description"},
		},
	}
}

// PromptDefFor returns the catalog entry for a key, or false.
func PromptDefFor(key string) (PromptDef, bool) {
	for _, p := range PromptCatalog() {
		if p.Key == key {
			return p, true
		}
	}
	return PromptDef{}, false
}
