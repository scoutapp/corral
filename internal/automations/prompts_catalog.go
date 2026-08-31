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
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	UsedWhen string   `json:"usedWhen"` // the "where is this used" callout
	Default  string   `json:"default"`  // built-in template
	Slots    []string `json:"slots"`    // slot names the code fills
}

// Prompt keys — the stable identifiers call sites use.
const (
	PromptProjectStart   = "project.start"
	PromptProjectIssue   = "project.issue"
	PromptPRVerify       = "pr.verify"
	PromptPRMerge        = "pr.merge"
	PromptAnalyzeBlock   = "analyze.block"
	PromptAnalyzeSummary = "analyze.summary"
	PromptRisk           = "pr.risk"
	PromptChatPreamble   = "chat.preamble"
	PromptDraftIssue     = "draft.issue"
	PromptDraftPrompt    = "draft.prompt"
	PromptSSHGuidance    = "ssh.guidance"
	PromptWorkerBoot     = "worker.boot_guidance"
	PromptEngPrinciples  = "engineering.principles"
	PromptRepoAgentsMd   = "repo.agents_md"
)

// DefaultEngineeringPrinciples is the built-in text of the engineering.principles
// prompt — a shared, editable snippet of working principles slotted into the
// project-start prompts (plain + from-issue) so Claude gets the same high bar on
// every sandbox task. Edited in ONE place; a repo can override it. Kept short and
// operational (the AGENTS.md generator restates these in its Definition of Done).
const DefaultEngineeringPrinciples = "Work to a high bar:\n" +
	"- Root cause, not symptoms: when something fails, diagnose WHY and fix the cause. Don't patch over the " +
	"symptom, silence the check, or special-case the failing input.\n" +
	"- Chesterton's fence: before removing or changing existing code, config, or a test, understand why it's " +
	"there. If you can't explain its purpose, investigate first — don't delete a guardrail just to make an error go away.\n" +
	"- Run the tooling before you finish: run the repo's linter/formatter and type checker (the same ones CI " +
	"runs) and get them green.\n" +
	"- Scope your tests: if the suite is large or slow, run the tests that cover the code you changed each " +
	"iteration; do a full run once at the end when feasible. Say so if you couldn't run the whole suite.\n" +
	"- Small, stacked commits: keep each change focused and reviewable. Prefer a stack of small commits/branches " +
	"over one large diff.\n" +
	"- Verify, don't assume: confirm the change works (a test, a build, running it) before calling it done."

// engPrinciplesSlot is the trailing slot the project prompts leave for the shared
// engineering-principles block (a leading blank line keeps spacing when present,
// nothing when the slot renders empty).
const engPrinciplesSlot = "{{engineering_principles}}"

// DefaultRepoAgentsMd is the built-in text of the repo.agents_md prompt — run as
// a headless host worker right after a repo is added (and on demand via
// "Regenerate"). It explores a throwaway checkout and writes the repo's agent
// context (AGENTS.md / CLAUDE.md), saved via `corral repo set-agent-context`.
//
// The template enforces the empirical quality bar for agent-onboarding docs (a
// BAD one is worse than none): tight (≤150 lines), command-first, only commands
// with evidence in the repo, no architecture essays, no wall of "don'ts". It also
// bakes in the same working principles as engineering.principles into the
// Definition of Done (root cause, Chesterton's fence, run the linter, scope tests
// when the suite is large, small stacked commits).
const DefaultRepoAgentsMd = "You are generating the agent-onboarding doc (an AGENTS.md / CLAUDE.md) for the repository {{repo}}. " +
	"This is what a coding agent reads FIRST before touching the code, so it must be concrete, verified against the " +
	"actual repo, and free of guesses. A BAD onboarding doc is worse than none — keep it tight and operational.\n\n" +
	"SETUP — get a working checkout you can explore and run:\n" +
	"1. Clone the repo's local mirror cache into a throwaway dir (the cache is a bare mirror, so a plain clone gives a working tree):\n" +
	"   git clone \"{{cache_path}}\" /tmp/agents-{{repoId}} && cd /tmp/agents-{{repoId}}\n" +
	"   The default branch is {{default_branch}}.\n" +
	"2. Explore the tree: README, package/build manifests, CI config, Makefile/Taskfile/scripts, and the top-level source layout. Read real files — don't assume conventions.\n\n" +
	"INVESTIGATE — DERIVE these from the repo, never invent them:\n" +
	"- The EXACT build, test, and run commands. Check package.json scripts, Makefile/Justfile/Taskfile targets, tox/nox, Cargo, go.mod, Gemfile/Rakefile, docker-compose, Procfile, etc. Prefer the repo's real script over a generic guess.\n" +
	"- How to actually RUN the app locally (dev server, CLI entrypoint, main binary) — the command a human uses to see it working, plus required services (DB, cache) and how to start them.\n" +
	"- CI: open the CI workflows (.github/workflows, .gitlab-ci.yml, …). Record what CI runs and the EXACT linter/formatter + type-check invocation, so an agent can run them locally BEFORE pushing.\n" +
	"- Test-suite size: roughly how many tests / how long a full run takes. If it's LARGE (minutes+), the Definition of Done must say to scope tests to the changed code rather than running everything each loop.\n" +
	"- Code style: point at the formatter/linter config and say \"run <tool>\" — do NOT restate style rules in prose.\n" +
	"- Files/paths an agent must NOT modify (generated code, vendored deps, lockfiles unless intentionally bumping, already-shipped migrations, snapshots).\n\n" +
	"WRITE the AGENTS.md with these sections, filled from what you found:\n" +
	"1. Project overview — what it is and does, in 3–5 sentences.\n" +
	"2. Repository layout — the directories that matter and what lives in each.\n" +
	"3. Build / Test / Run — the EXACT copy-pasteable commands, with prerequisites (runtime version via mise/asdf, services to start) and how to run the app to see it working.\n" +
	"4. Code style — point at the formatter/linter config; the rule is \"run the tooling\", not a hand-maintained list.\n" +
	"5. CI & linting — what CI enforces and the exact local commands to satisfy it before pushing.\n" +
	"6. Files not to modify — the do-not-touch list.\n" +
	"7. Definition of Done — a checklist an agent must satisfy before a task is complete:\n" +
	"   - The change builds and the relevant tests pass. If the suite is large, run the tests that cover the changed code each loop, not the whole suite; do a full run once at the end if feasible.\n" +
	"   - The linter/formatter and type checker pass (run the same ones CI runs).\n" +
	"   - Fixes address the ROOT CAUSE, not the symptom: diagnose WHY the failure happens and fix that, rather than patching over it or silencing the check.\n" +
	"   - Chesterton's fence: before removing or changing existing code/config/tests, understand WHY it's there; if you can't, investigate first — don't delete guardrails to make an error go away.\n" +
	"   - Keep changes small and reviewable; prefer stacked, focused commits over one large diff.\n\n" +
	"CONSTRAINTS (this is the quality bar — follow it):\n" +
	"- Target UNDER 150 lines. Command-first and skimmable — an agent reads this every time, so favor exact commands over prose.\n" +
	"- Every command must be one you found EVIDENCE for in the repo (a script, a CI step, documented usage). Do NOT invent commands. If you can't determine something, say so explicitly rather than guessing.\n" +
	"- Do NOT write architecture essays or service-topology dumps, do NOT pile up 30+ \"don't\" rules, and do NOT duplicate the README. Prefer paired Don't → Do and real 3–10 line snippets over abstract rules.\n\n" +
	"SAVE — write the finished document as this repo's agent context so it's editable in Corral and injected into future sandboxes:\n" +
	"   corral repo set-agent-context {{repoId}} --stdin   # pipe the full markdown on stdin\n" +
	"Then remove the throwaway checkout: rm -rf /tmp/agents-{{repoId}}\n" +
	"Report a one-line summary of what you wrote (and anything you couldn't determine)."

// DefaultWorkerBootGuidance is the built-in text of the worker.boot_guidance
// prompt — appended to every worker's prompt so a boot makes the DinD baseline
// reuse fast. Generic by default (any repo/stack); a repo can OVERRIDE it in the
// Prompts section with its own exact recipe (our Rails app names its volumes,
// etc.). Kept as a catalog prompt so it's editable in one place, per-repo.
const DefaultWorkerBootGuidance = "MAKE EXPENSIVE WORK REUSABLE: Corral snapshots a project's inner-docker on clean " +
	"stop into a per-repo baseline that future projects reuse — but a snapshot captures IMAGES and NAMED VOLUMES, " +
	"not a running container's writable layer. Put EVERY slow, reusable step where the snapshot can capture it, so " +
	"the next project from this repo skips it (examples use <app>-… — substitute this app's name):\n" +
	"  • Dependencies (bundle/npm/pip/go mod): install into a NAMED VOLUME the app mounts (e.g. " +
	"`-v <app>-deps:/usr/local/bundle`), not a bare container layer.\n" +
	"  • Datastore: run it with its data dir on a NAMED VOLUME (`-v <app>-db:/var/lib/postgresql/data`) and migrate " +
	"ONCE — the migrated DB is then captured, so reuse skips create+migrate (often the biggest per-boot cost).\n" +
	"  • App warmup: after deps + any build/asset step are ready, `docker commit` the prepared container to an image " +
	"(e.g. `<app>-prepared:latest`) so compile/eager-load/asset work is baked in; cache any per-boot build cache in a " +
	"named volume too (Rails bootsnap+assets, Node .next, Go/Rust build caches, etc.).\n" +
	"The goal: a reused boot should only START containers + boot the app, not rebuild/reinstall/re-migrate. Name " +
	"volumes deterministically (stable per-app) so the next project remounts them."

// DefaultSSHPushGuidance is the built-in text of the ssh.guidance prompt — the
// sentence telling Claude to push over the SSH remote (the HTTPS remote won't
// authenticate in the sandbox; the project's scoped ssh-agent holds the key).
// It is an EDITABLE catalog prompt (PromptSSHGuidance): the project prompts fill
// their {{ssh_guidance}} slot with this rendered text (with {{ssh_remote}} →
// git@github.com:<repo>.git) when a key is configured, or "" when not — so it
// appears only when it applies, and its wording is user-editable in one place.
const DefaultSSHPushGuidance = "When you push, use the SSH remote ({{ssh_remote}}) — the scoped ssh-agent has the key; the HTTPS remote won't authenticate here."

// sshGuidanceSlot is the trailing slot the project prompts leave for the SSH
// sentence (a leading space keeps the spacing right when present, nothing when
// empty).
const sshGuidanceSlot = "{{ssh_guidance}}"

// PromptCatalog returns every editable prompt with its built-in default, in
// display order. This is the single source of truth for the carousel UI and the
// call sites (which look up their default here rather than inlining it).
func PromptCatalog() []PromptDef {
	return []PromptDef{
		{
			Key:      PromptProjectStart,
			Name:     "Project start",
			UsedWhen: "Typed into Claude when a plain sandbox project launches (New project, or Verify-in-sandbox without a preset).",
			Default: "You're working in a sandboxed checkout of {{repo}} on branch {{branch}}. " +
				"Explore the codebase, then help with the task at hand. " + sshGuidanceSlot + "\n\n" + engPrinciplesSlot,
			Slots: []string{"repo", "branch", "ssh_guidance", "engineering_principles"},
		},
		{
			Key:      PromptProjectIssue,
			Name:     "Project start (from an issue)",
			UsedWhen: "Typed into Claude when a project is created from a GitHub issue.",
			Default:  "Work on {{repo}} issue #{{number}}: {{title}}. The full description is in ISSUE.md at the workspace root. You're on branch {{branch}}. " + sshGuidanceSlot + "\n\n" + engPrinciplesSlot,
			Slots:    []string{"repo", "number", "title", "branch", "ssh_guidance", "engineering_principles"},
		},
		{
			Key:      PromptPRVerify,
			Name:     "Verify PR in sandbox",
			UsedWhen: "Auto-submitted to Claude when you click ▶ Verify in sandbox on a PR (using the built-in prompt).",
			Default: `Verify PR #{{pr_number}} ("{{pr_title}}") works. You're on its branch. ` +
				"Explore the change, run the relevant tests or the app, and report whether it behaves correctly " +
				"and any issues you find. Run the repo's linter/type-check as part of verifying. If it fails, find " +
				"the ROOT CAUSE before proposing a fix — don't patch the symptom. The PR is {{pr_url}}.",
			Slots: []string{"pr_number", "pr_title", "pr_url"},
		},
		{
			Key:      PromptPRMerge,
			Name:     "Rebase & merge PR in sandbox",
			UsedWhen: "Auto-submitted to Claude when you click \"Rebase & merge in sandbox\" on a PR. Runs in a one-shot sandbox checked out on the PR's branch; the sandbox is torn down once the PR is merged (if auto-teardown is on).",
			Default: "You're in a sandboxed checkout of {{repo}} on PR #{{pr_number}}'s branch ({{branch}}). " +
				"Your job: get this PR mergeable via **{{strategy}}** and merge it, following best practices. " +
				"The PR is {{pr_url}} (\"{{pr_title}}\"). The base branch is {{default_branch}}.\n\n" +
				"Procedure:\n" +
				"1. Fetch and update your local view of the base: `git fetch origin` and note `origin/{{default_branch}}`.\n" +
				"2. Rebase this branch onto the latest base: `git rebase origin/{{default_branch}}`.\n" +
				"3. Resolve any conflicts carefully — understand WHY each side made its change before you pick (Chesterton's fence), don't blindly take one. Re-run the build/tests after resolving so you know the result still works.\n" +
				"4. Push the rebased branch (force-with-lease, since a rebase rewrites history): `git push --force-with-lease`.\n" +
				"5. Check whether the PR has CI. Run `gh pr checks {{pr_number}}`: if it reports checks, WAIT for them to finish and pass — `gh pr checks {{pr_number}} --watch` blocks until they complete and exits non-zero if any fail. If checks fail, STOP and report which ones — do NOT merge. (No checks configured → nothing to wait for; continue.)\n" +
				"6. Once checks are green, complete the merge on GitHub with **{{strategy}}**: `gh pr merge {{pr_number}} --{{strategy}}`. Do NOT use `--admin` or any force/override flag — merge as a normal user so branch protection and required checks are respected. If GitHub refuses the merge (not mergeable, checks required, review required), STOP and report why rather than trying to bypass it.\n" +
				"7. If this PR sits in a STACK of dependent branches, work from the bottom up: after merging the base of the stack, go back to step 1 for the next branch, rebasing it onto the freshly-updated {{default_branch}} and re-targeting its base if needed (each still waits for its own CI). Keep the stack as small, focused branches — don't collapse unrelated work into one.\n" +
				"8. When resolving conflicts across a stack, before dropping any branch as redundant, get the diff stats between the branches (`git diff --stat A...B`) to confirm they really are equivalent — keeping in mind that a rebase changes commit shas, so compare TREES/diffs, not commit identity. Only drop a branch when its changes are genuinely already present.\n\n" +
				"Be conservative: if a conflict is ambiguous, CI fails, or the tests don't pass after a rebase, STOP and report what you found rather than forcing a merge. Never use admin/override flags to merge past failing checks. Report the final state (merged / blocked and why) when you're done.",
			Slots: []string{"repo", "branch", "strategy", "pr_number", "pr_title", "pr_url", "default_branch"},
		},
		{
			Key:      PromptAnalyzeBlock,
			Name:     "Analyze — block risk",
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
			Key:      PromptAnalyzeSummary,
			Name:     "Analyze — PR summary",
			UsedWhen: "Sent during Analyze with AI to produce the short (<100 char) PR summary.",
			Default: "Summarize this pull request in under 100 characters.\n" +
				"PR Title: {{pr_title}}\n" +
				"Key changes: {{key_changes}}\n" +
				"Return only the summary string, no quotes or extra text.",
			Slots: []string{"pr_title", "key_changes"},
		},
		{
			Key:      PromptRisk,
			Name:     "PR risk assessment",
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
			Key:      PromptChatPreamble,
			Name:     "Ask Claude — context preamble",
			UsedWhen: "Prepended to the first message when you Ask Claude about a PR or block. {{context}} holds the PR summary, block diff, and hot files Corral assembles; wrap it with your own instructions.",
			// Default is passthrough — the assembled {{context}} already contains
			// the intro + focus lines, so an unedited preamble changes nothing. A
			// user override wraps/replaces the framing around {{context}}.
			Default: "{{context}}",
			Slots:   []string{"context"},
		},
		{
			Key:      PromptDraftIssue,
			Name:     "Draft an issue with AI — instructions",
			UsedWhen: "The instruction given to the host Claude when you click Draft with AI on a new issue (before it researches the repo).",
			Default: "You are drafting a GitHub issue for this repository. " +
				"Research the codebase (read relevant files, grep, explore the structure) so the issue is concrete and grounded in the actual code. " +
				"The user's request:\n\n{{description}}\n\n" +
				"Investigate, then briefly summarize what you found and what the issue should cover. Do not write the final issue yet.",
			Slots: []string{"description"},
		},
		{
			Key:      PromptDraftPrompt,
			Name:     "Build a prompt with AI — instructions",
			UsedWhen: "The instruction given to the host Claude when you click Build with AI on the project-start prompt.",
			Default: "You are writing a project-start prompt: the first instruction given to Claude Code when it " +
				"opens a sandboxed checkout of a repository to work on it. " +
				"The prompt is a reusable TEMPLATE; you may include {{repo}}, {{branch}}, {{pr_number}}, {{pr_title}}, {{pr_url}} placeholders.\n\n" +
				"The user wants a prompt that: {{description}}\n\n" +
				"Research this codebase so the prompt is concrete, then briefly summarize what you found. Do not write the final prompt yet.",
			Slots: []string{"description"},
		},
		{
			Key:      PromptSSHGuidance,
			Name:     "SSH push guidance",
			UsedWhen: "Added to the project-start prompts (plain + from-issue) when an SSH key is configured, so Claude pushes over the SSH remote instead of HTTPS. Omitted when no key is set.",
			Default:  DefaultSSHPushGuidance,
			Slots:    []string{"ssh_remote"},
		},
		{
			Key:      PromptWorkerBoot,
			Name:     "Worker boot & caching guidance",
			UsedWhen: "Appended to every conductor worker's prompt (per-repo when the worker is created with a repoId) so booting an app makes the DinD baseline reuse fast. Edit the repo override to give this repo its exact recipe (volume names, DB setup, prepared image).",
			Default:  DefaultWorkerBootGuidance,
			Slots:    []string{},
		},
		{
			Key:      PromptEngPrinciples,
			Name:     "Engineering principles",
			UsedWhen: "Slotted into the project-start prompts (plain + from an issue). Edit once to change the working principles Claude gets on every sandbox task — root-cause fixes, Chesterton's fence, run the linter, scope tests, small stacked commits.",
			Default:  DefaultEngineeringPrinciples,
			Slots:    []string{},
		},
		{
			Key:      PromptRepoAgentsMd,
			Name:     "Generate AGENTS.md (on repo add)",
			UsedWhen: "Run as a headless host worker right after a repo is added (and on demand via \"Regenerate AGENTS.md\" in repo Settings). It explores a throwaway checkout and writes the repo's agent context (CLAUDE.md), which becomes editable in Settings and is injected into future sandboxes.",
			Default:  DefaultRepoAgentsMd,
			Slots:    []string{"repo", "repoId", "cache_path", "default_branch"},
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
