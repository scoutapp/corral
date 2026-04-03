---
name: agent-team
description: Multi-agent development team structure — Planner, Worker, and Tester agents running in parallel tmux panes, each with an Evaluator sub-agent.
---

# Agent Team

This project runs a three-agent development team in parallel tmux panes. Each agent has a distinct role and spawns an Evaluator sub-agent to review its work before acting.

## Team Structure

```
┌─────────────────┬─────────────────┬─────────────────┐
│    Planner      │     Worker      │     Tester      │
│                 │                 │                 │
│  User input →   │  /loop issues → │  /loop PRs →    │
│  GitHub issues  │  implement →    │  test plan →    │
│                 │  evaluator →    │  evaluator →    │
│  evaluator →    │  PR             │  sign off       │
│  gh issue create│                 │                 │
└─────────────────┴─────────────────┴─────────────────┘
```

## Agents

### Planner
- **Input**: High-level goal from the user
- **Output**: GitHub issues (`gh issue create`)
- **Evaluator focus**: Project coherence — are issues well-scoped and complete? (not technical)
- **Principle**: Keep issues simple. Title + 2-3 bullets. Leave implementation to the Worker.

### Worker
- **Input**: Open GitHub issues (via `/loop 5m`)
- **Output**: Draft PRs on GitHub
- **Evaluator focus**: Code quality, conventions, refactoring opportunities, PR scope
- **Principle**: One issue per PR. Keep PRs minimal. Split naturally when needed and link the chain.

### Tester
- **Input**: Open PRs (via `/loop 5m`)
- **Output**: PR comments + approval/change request
- **Evaluator focus**: Test plan completeness, security gaps, missing edge cases
- **Principle**: Cover what matters — unit tests, browser flows, security, edge cases. Follow PR chains.

## Evaluator Pattern

Each agent spawns an Evaluator sub-agent before taking a significant action. The agent uses the
built-in `Agent` tool to call its evaluator, passing the draft work (issues, code diff, test plan)
and asking for critique. The agent then incorporates the feedback before proceeding.

Evaluators are defined via the `--agents` flag at startup and are available as named sub-agents
during the session.

## PR Chains

The Worker may split work into multiple linked PRs when it makes sense (e.g., implementation + tests
in separate PRs). When this happens:
- Each PR references the others in its description: "Part 1 of 2 — see #456 for tests"
- The Tester follows the chain and verifies the full set before signing off
- The Tester comments on each PR in the chain linking back to the test results

## Communication Channel

All inter-agent communication goes through GitHub:
- Planner → Worker: GitHub issues
- Worker → Tester: GitHub PRs (draft → ready)
- Tester → Worker: PR comments and review decisions

## Starting the Team

The team is started automatically by `launcher.py` when `AGENT_TEAMS_ENABLED=1`. Each agent
receives its role via `--append-system-prompt` and its evaluator sub-agent via `--agents`.

From the host:
```bash
AGENT_TEAMS_ENABLED=1 ./sandclaude start
```

## Loop Intervals

Both the Worker and Tester use `/loop 5m` to check for new work every 5 minutes. Adjust the
interval in the prompt files or via `/loop` in the pane directly if needed.
