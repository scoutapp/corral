#!/bin/bash
# Claude Code Stop hook: enforces small commits.
# Blocks Claude from stopping when uncommitted changes exceed the line threshold.
# Runs after every conversation turn via the Stop hook configured in settings.json.

THRESHOLD=300

# Not a git repo: nothing to enforce
GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$GIT_ROOT"

# Count total changed lines (insertions + deletions) not yet in HEAD
TOTAL=$(git diff HEAD --shortstat 2>/dev/null \
    | grep -oE '[0-9]+ (insertion|deletion)' \
    | grep -oE '[0-9]+' \
    | awk '{s+=$1} END {print s+0}')

# New repo with no commits yet: fall back to checking the index
if [ -z "$TOTAL" ] || [ "$TOTAL" = "0" ]; then
    TOTAL=$(git diff --cached --shortstat 2>/dev/null \
        | grep -oE '[0-9]+ (insertion|deletion)' \
        | grep -oE '[0-9]+' \
        | awk '{s+=$1} END {print s+0}')
fi

TOTAL=${TOTAL:-0}

# Count lines in untracked files (not ignored) so new files can't slip past the threshold
UNTRACKED=$(git ls-files --others --exclude-standard 2>/dev/null \
    | xargs wc -l 2>/dev/null \
    | tail -1 | awk '{print $1}')
TOTAL=$((TOTAL + ${UNTRACKED:-0}))

if [ "$TOTAL" -gt "$THRESHOLD" ]; then
    CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
    printf '{"decision":"block","reason":"Small commit enforcement: %d uncommitted lines (threshold: %d) on branch '\''%s'\''. Create a stacked branch for this unit of work: (1) git checkout -b feat/<description> to branch off the current branch, (2) git add <files> and git commit -m '\''type: description'\'' for this logical change, (3) push and open a PR targeting '\''%s'\''. Each branch in the stack should be one focused change under %d lines."}\n' \
        "$TOTAL" "$THRESHOLD" "$CURRENT_BRANCH" "$CURRENT_BRANCH" "$THRESHOLD"
    exit 2
fi

exit 0
