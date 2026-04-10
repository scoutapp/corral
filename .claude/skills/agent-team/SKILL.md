---
name: agent-team
description: Multi-agent development team structure — three separate Claude Code agent teams (Orchestrator, Worker, Tester) running in parallel tmux panes. Teams communicate only through GitHub.
---

# Agent Team

Three separate Claude Code sessions run in parallel tmux panes. Each session is told to **"Create an agent team to..."** and spawns its own teammates. The three teams have no direct communication — they are completely independent and coordinate only through GitHub issues, PR labels, and PR comments.

## Team Structure

```
┌──────────────────────┬──────────────────────┬──────────────────────┐
│  Orchestrator Team   │    Worker Team       │    Tester Team       │
│                      │                      │                      │
│  Create an agent     │  Create an agent     │  Create an agent     │
│  team to manage the  │  team to implement   │  team to review and  │
│  project backlog...  │  GitHub issues...    │  test pull requests  │
│                      │                      │                      │
│  teammates:          │  teammate:           │  teammate:           │
│  • evaluator         │  • evaluator         │  • evaluator         │
│    (reviews issues)  │    (reviews code)    │    (directs testing  │
│  • foreman           │                      │     + reviews quality│
│    (Monitor 2m,      │  Monitor watcher →   │                      │
│     checks progress, │  git worktree →      │  Monitor watcher →   │
│     merges)          │  implement →         │  reads PR desc →     │
│                      │  implement →         │  reads PR desc →     │
│  asks user sparingly │  evaluate → PR       │  evaluate → test →   │
│                      │  label: ready for    │  evaluate quality →  │
│                      │  review              │  label: needs rev /  │
│                      │                      │  ready for merge     │
└──────────────────────┴──────────────────────┴──────────────────────┘
                   ↕ GitHub only ↕              ↕ GitHub only ↕
```

## How each team is started

Each session receives a `--append-system-prompt` for context/constraints, then is given a **"Create an agent team to..."** prompt that describes the team's purpose and the roles teammates should play. Claude structures the team and spawns teammates based on that description.

`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` is set in the environment, and `--teammate-mode tmux` opens each teammate in a split pane within the current tmux window.

## Orchestrator Team

**Purpose**: Translate user goals into GitHub issues; maintain the project backlog.

**Start prompt style**: `"Create an agent team to manage the project backlog and translate user goals into GitHub issues. The team should have: one teammate as an evaluator [reviews issue drafts for coherence, one round]... one teammate as a foreman [loops every 2m, checks progress, squash-merges ready-for-merge PRs when they match project goals]..."`

**User interaction**: The Orchestrator may ask the user for input, but sparingly — only for overall feature direction, never implementation decisions.

**Foreman responsibilities**:
- Uses Monitor to stream events from a background watcher (60s poll) reading open issues, PR labels, merged PRs
- Squash-merges PRs labeled `ready for merge` if they match the project goal (`gh pr merge <n> --squash`)
- If `part of larger feature`: waits for all related PRs before merging to feature branch
- Advises Orchestrator on whether feature flags can isolate incomplete work in main

## Worker Team

**Purpose**: Pick up GitHub issues, implement them in git worktrees, create PRs.

**Start prompt style**: `"Create an agent team to implement GitHub issues and deliver pull requests. The team should have one teammate as a code review evaluator [reviews code before PR, checks conventions/DRY/scope, one round]..."`

**Autonomous**: Never asks user for input.

**Key behaviors**:
- Monitor tool on a background watcher script (60s poll) — checks for `needs revision` PRs first (priority), then new issues
- Creates a **true git worktree** per branch: `git worktree add ../worktree-<branch> -b <branch>` — not Claude's built-in worktree
- PRs: target +300/-300 lines, split by domain (not file type), linked when inseparable
- Trunk-based development; use feature flags to isolate incomplete features in main
- After pushing: adds `ready for review` label + brief PR comment (1-3 sentences)
- **Nitpicky revisions**: if `needs revision` feedback is purely cosmetic (naming, whitespace, style) with no logic/security/best-practices issues — skip the changes, remove `needs revision`, add `ready for merge`, note why
- **Never amend**: always `git commit` (new commit) for revisions, never `git commit --amend`; rebase downstream branches after pushing

## Tester Team

**Purpose**: Review PRs labeled `ready for review`, test them, update labels.

**Start prompt style**: `"Create an agent team to review and test pull requests. The team should have one teammate as a testing evaluator with a reversed role: it directs the tester before testing begins AND reviews test quality afterward..."`

**Autonomous**: Never asks user for input.

**Key behaviors**:
- Monitor tool on a background watcher script (60s poll) — picks up `ready for review` PRs
- Evaluator directs approach first: what to test, whether Chromium/Playwright is needed, edge cases
- Evaluator reviews quality after: are tests meaningful or self-asserting?
- Posts concise PR comment, then updates label to `needs revision` (with specifics) or `ready for merge`
- For PR chains (linked PRs): reads all linked descriptions before testing
- **One round only**: give one round of general feedback; do not loop on the same PR repeatedly
- **Only block for fundamental issues**: `needs revision` only for logic errors, broken functionality, or security issues — cosmetic feedback goes in the comment but label is `ready for merge`

## PR Label Lifecycle

```
Worker pushes PR → [ready for review]
                         ↓
                   Tester reviews
                    ↙          ↘
         [needs revision]   [ready for merge]
               ↓                    ↓
         Worker fixes         Foreman checks goal
               ↓                    ↓
       [ready for review]    squash merge / feature branch
```

**Labels**:
- `ready for review` — Worker done, Tester picks up
- `needs revision` — Tester found issues; Worker prioritizes this over new tickets
- `ready for merge` — Tester approved; Foreman evaluates goal alignment and merges
- `part of larger feature` — hold for feature branch; Foreman waits for full set

## Context Efficiency

- PR descriptions: specific and structured — what problem, what approach, linked PRs. Not verbose.
- PR comments: 1-3 sentences — enough to rebuild context, not a wall of text
- When resuming `needs revision`: read the tester's comment first, not the full diff
- When testing: read PR description and Worker comments before fetching diffs

## Issue Monitoring Mode

A single session with 4 teammates (worker, worker-evaluator, tester, tester-evaluator). Same quality rules apply, but:
- **No auto-merge**: label `ready for merge` and stop — a human reviews and merges
- Same nitpicky-skip rule for worker; same one-round / fundamental-only rule for tester

## Starting the Teams

```bash
AGENT_TEAMS_ENABLED=1 ./sandclaude start
```

Three tmux panes open: Orchestrator | Worker | Tester. Each independently creates its own agent team.

Each script runs `patch-claude-settings.py` before starting Claude to configure `skipDangerousModePermissionPrompt`, `tmuxSplitPlanes`, and `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`.
