---
name: small-commits
description: Git commit discipline with stacked branches — a Stop hook runs after every conversation turn and blocks Claude from finishing when uncommitted changes exceed 300 lines. Each unit of work belongs on its own branch stacked off the previous one. Use this skill when you need to understand or adjust commit/branch behavior.
---

# Small Commits — Stacked Branches

A **Stop hook** (`/home/claude/bin/enforce-small-commits.sh`) runs after every conversation turn. If uncommitted changes exceed **300 lines** (insertions + deletions), it blocks the stop and tells Claude to create a stacked branch for that unit of work.

## The stacking model

Each logical unit of work gets its own branch, stacked on the previous:

```
main
 └── feat/auth-model          (PR → main)
      └── feat/auth-routes    (PR → feat/auth-model)
           └── feat/auth-tests (PR → feat/auth-routes)
```

Each branch holds **one focused commit** under 300 lines. PRs target the parent branch, not main. When the parent merges, GitHub auto-retargets child PRs.

## When the hook fires

You'll see a block message naming the current branch and the stacking steps:

> Small commit enforcement: 420 uncommitted lines (threshold: 300) on branch 'feat/auth-model'. Create a stacked branch for this unit of work...

**Resolve it:**

```bash
# 1. Branch off the current branch (not main)
git checkout -b feat/auth-routes

# 2. Stage and commit one logical change
git add src/routes/auth.ts tests/routes/auth.test.ts
git commit -m "feat(auth): add login and logout routes"

# 3. Push and open a PR targeting the parent branch
gh pr create --base feat/auth-model --title "feat(auth): add login and logout routes"
```

## Branch naming

Use conventional-style names that describe what the branch does:

```
feat/<scope>-<action>     feat/auth-model, feat/auth-routes
fix/<scope>-<what>        fix/auth-token-expiry
refactor/<scope>-<what>   refactor/auth-extract-middleware
```

## Commit conventions

- Format: `type(scope): description` (conventional commits)
- One logical change per commit — feature, fix, refactor, test, docs
- **Never amend commits** — always create new ones for revisions
- If downstream branches exist and you revise a parent, rebase them: `git rebase <revised-parent>`

## Splitting an oversized diff

If you have > 300 lines across unrelated concerns, split by domain:

```bash
# Commit the model layer first
git checkout -b feat/user-model
git add src/models/user.ts
git commit -m "feat(user): add user model and schema"

# Stack the service layer on top
git checkout -b feat/user-service
git add src/services/user.ts tests/services/user.test.ts
git commit -m "feat(user): add user service with CRUD operations"
```

**Right split:** by what it accomplishes (model vs. service vs. routes)  
**Wrong split:** by file type (all `.ts` vs. all `.test.ts`)

## Technical details

- **Trigger:** Claude Code `Stop` hook — runs once per turn when Claude finishes
- **Measurement:** `git diff HEAD --shortstat` (insertions + deletions vs HEAD)
- **Fallback:** `git diff --cached` for repos with no commits yet
- **Script:** `/home/claude/bin/enforce-small-commits.sh` (volume-mounted from `bin/`)
- **Settings:** injected into `~/.claude/settings.json` by `bin/patch-claude-settings.py` at startup
