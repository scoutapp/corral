---
name: evaluate
description: Spawn an evaluator sub-agent to critique your current work (issues, code, test plan) before acting. Use this to get a second opinion and catch blind spots.
---

# Evaluate

Use this skill to invoke your evaluator sub-agent and get a critique of your current draft work
before you commit to it. The evaluator looks for problems, gaps, and improvements.

## When to use

- **Planner**: After drafting GitHub issues, before calling `gh issue create`
- **Worker**: After writing code, before calling `gh pr create`
- **Tester**: After writing a test plan, before executing tests

## How it works

Each agent has an `evaluator` sub-agent defined via `--agents` at startup. To invoke it:

1. Prepare a clear summary of what you're about to do (issue list, code diff, test plan)
2. Use the Agent tool with the `evaluator` agent name
3. Pass the draft work and ask: "What's missing? What should I change?"
4. Read the feedback and decide what to incorporate
5. Proceed with the improved version

## Example invocation (in your thinking)

```
I'll ask my evaluator to review these 4 issue drafts before I create them in GitHub.

[Agent tool call: evaluator]
"Here are the 4 issues I'm about to create. Review them for project coherence:

1. Add user authentication — implement login/logout with session management
2. Create dashboard view — show key metrics on the home page after login
3. Add CSV export — allow users to download their data as CSV
4. Fix broken pagination — pagination buttons don't work on the issues list

Are these well-scoped? Any gaps? Anything unclear?"
```

## What evaluators look for

| Agent   | Evaluator focus |
|---------|----------------|
| Planner | Project coherence: scope, gaps, clarity of issues |
| Worker  | Code quality: conventions, refactoring, PR scope |
| Tester  | Test coverage: edge cases, security, missing scenarios |

## Incorporating feedback

Evaluators are opinionated — treat their feedback as input, not orders. Use judgement:
- Act on feedback that catches real problems
- Skip feedback that would cause scope creep or perfectionism
- When in doubt, lean toward action over endless refinement
