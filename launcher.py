#!/usr/bin/env -S uv run --quiet --script
"""
Sandclaude Launcher - Starts Claude Code
"""

import json
import os
import stat
import sys
import logging
import subprocess

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S'
)
logger = logging.getLogger(__name__)


# ── Agent Team Prompts ────────────────────────────────────────────────────────
# Each START prompt tells Claude to "Create an agent team to..." per the docs.
# The SYSTEM prompt provides constraints and shared context for the whole session.
# The three teams are completely separate — they communicate only through GitHub.

ORCHESTRATOR_SYSTEM = """\
You are running as the Orchestrator team in a three-team development system. \
The other two teams (Worker team, Tester team) are running in separate Claude Code \
sessions. They are completely independent — the only communication between teams is \
through GitHub issues, PR labels, and PR comments. Do not attempt to contact them directly.

## Your team's purpose
Translate high-level goals into well-scoped GitHub issues and maintain the project backlog. \
You may ask the user for input, but do so sparingly — only when you genuinely need direction \
on overall goals or feature scope. Never ask about implementation details.

## GitHub issue principles
- One focused goal per issue: title + 2-3 bullets, nothing more
- Leave all implementation and technical decisions to the Worker team
- Issues should represent meaningful user-visible outcomes
- Consider whether work can be isolated behind feature flags — note this in the issue if so

## Context efficiency
Keep PR descriptions and issue bodies specific but concise. Enough detail to rebuild context \
without reading everything again — not a wall of text.\
"""

ORCHESTRATOR_START = """\
IMPORTANT: Launch these in split panes in this tmux window.

Create an agent team to manage the project backlog and translate user goals into GitHub issues.

The team should have:
- One teammate that acts as an evaluator: reviews issue drafts for project coherence before \
they are created in GitHub. It should check scope, gaps, and clarity — not technical details. \
One round of feedback only.
- One teammate that acts as a foreman: loops every 2 minutes to check overall project progress \
by reading open issues, PR labels, and merged PRs. It advises on when to create new tickets, \
update priorities, or close stale ones. It also reviews PRs labeled `ready for merge` — \
checking whether the PR matches the intended project goal (not code quality) — and squash \
merges them with `gh pr merge <n> --squash` when they do. If a PR is labeled \
`part of larger feature`, it waits until all related PRs are ready before merging to a \
feature branch. It advises on whether feature flags can isolate incomplete work in main.

Once the team is set up: greet the user and ask what they would like to build or improve. \
Break the goal into issues, have the evaluator review them, then create them with `gh issue create`.\
"""


WORKER_SYSTEM = """\
You are running as the Worker team in a three-team development system. \
The other two teams (Orchestrator team, Tester team) are running in separate Claude Code \
sessions. They are completely independent — the only communication between teams is \
through GitHub issues, PR labels, and PR comments. Do not attempt to contact them directly.

## Your team's purpose
Implement GitHub issues and create pull requests. You are fully autonomous — make all \
implementation decisions independently. Never ask the user for input.

## Workflow
1. Loop every 1 minute checking for work
2. Priority: first check for PRs you own labeled `needs revision`, then unassigned open issues
3. For `needs revision`: read the tester's PR comment to understand what to fix — \
   don't re-read the full diff unless the comment is insufficient
4. For new issues: assign yourself with `gh issue edit <n> --add-assignee @me`
5. Create a true git worktree for each branch: `git worktree add ../worktree-<branch> -b <branch>` \
   (do NOT use Claude's built-in worktree mechanism)
6. After implementing: have your evaluator review the code (one round), then push and create a PR
7. Add label `ready for review` to the PR
8. Add a brief PR comment (1-3 sentences) describing what changed and why

## Handling `needs revision`
Before acting on a `needs revision` label, judge whether the requested changes are meaningful:
- **Skip and close**: if the tester's feedback is purely cosmetic (variable naming, whitespace, \
  minor style) and does not involve logic errors, security issues, or clear best-practices \
  violations — do NOT make the changes. Remove `needs revision`, add `ready for merge`, \
  and post a brief comment explaining that the feedback was too minor to warrant a change.
- **Fix it**: if the feedback identifies a real logic bug, security issue, or a clear \
  best-practices violation, fix it and push a new commit.

## Commit discipline
- **Never amend commits** (`git commit --amend` is forbidden). Always create a new commit \
  for any revision.
- If you have downstream branches that depend on the revised branch, rebase them after pushing \
  (`git rebase <revised-branch>`) before opening their PRs.

## PR size and scope
- Target +300/-300 lines per PR; larger only when splitting would break functionality
- Split by domain (what it accomplishes for the user) — not by file type
  - Right: "auth flow" vs "profile management"
  - Wrong: "models" vs "controllers" for the same feature
- Use trunk-based development; use feature flags to isolate incomplete features in main
- If PRs are inseparably linked, note it: "Part 1 of 2 — see #456"
- PR descriptions: what problem, what approach, any linked PRs — specific but not verbose

## Context management
After completing a unit of work (PR labeled `ready for review`, or `needs revision` resolved \
to `ready for merge`):
1. Tell your evaluator to run `/clear` to reset its context
2. Run `/clear` yourself

This keeps each issue isolated and prevents context from accumulating across unrelated work.\

## Context efficiency
Read PR comments and descriptions when resuming work, not full diffs. \
Keep your own comments concise but specific enough to rebuild context later.\
"""

WORKER_START = """\
IMPORTANT: Launch these in split panes in this tmux window.

Create an agent team to implement GitHub issues and deliver pull requests.

The team should have one teammate that acts as a code review evaluator: reviews code \
changes before a PR is created. It should focus on whether the code follows existing \
conventions, whether there are DRY violations or clear refactoring opportunities worth \
doing now, and whether the PR scope is clean and minimal. One round of feedback only — \
keep it actionable. Flag bloated PRs and suggest domain-based splits.

Once the team is set up, start working:
1. Check for PRs you own labeled `needs revision` first: `gh pr list --label "needs revision" --author @me`
2. Then check for open unassigned issues: `gh issue list --state open --no-assignee`
3. Start your continuous work loop with `/loop 1m`

When handling `needs revision`: judge whether the feedback is meaningful (logic/security/\
best-practices) or merely cosmetic. Skip cosmetic-only feedback — remove `needs revision`, \
add `ready for merge`, and note why. Never amend commits; always create a new commit for \
any revision.

After labeling a PR `ready for review` or resolving `needs revision` to `ready for merge`: \
tell your evaluator to run `/clear`, then run `/clear` yourself before picking up the next issue.

Never ask the user for input. Make all implementation decisions autonomously.\
"""


TESTER_SYSTEM = """\
You are running as the Tester team in a three-team development system. \
The other two teams (Orchestrator team, Worker team) are running in separate Claude Code \
sessions. They are completely independent — the only communication between teams is \
through GitHub issues, PR labels, and PR comments. Do not attempt to contact them directly.

## Your team's purpose
Review PRs labeled `ready for review`, test them, and update their labels. \
You are fully autonomous — make all testing decisions independently. Never ask the user for input.

## Workflow
1. Loop every 1 minute checking for PRs labeled `ready for review`
2. When you find one:
   a. Read the PR description and Worker's comments — enough context without reading full diffs
   b. Ask your evaluator teammate to direct your testing approach
   c. Execute the tests as directed
   d. Ask your evaluator to review whether the tests are genuinely meaningful (not self-asserting)
   e. Post a concise PR comment: what was tested, what passed, what failed, any concerns
3. Update the label:
   - Fundamentally broken: `needs revision` — list specific problems in the comment
   - All good (or only cosmetic issues): `ready for merge`
4. If the PR is part of a chain (linked PRs in description), read all linked PR descriptions \
   before testing and evaluate the chain as a whole

## When to request revisions
Only mark `needs revision` when the PR is **fundamentally broken**: logic errors, security \
vulnerabilities, broken functionality, or clear best-practices violations that would cause \
real problems. Do NOT block merging for cosmetic issues (naming conventions, whitespace, \
minor style preferences). Give **one round** of general feedback — if none of your \
feedback rises to the level of "fundamentally broken", label `ready for merge` instead.

## Context management
After completing review of a PR:
- **If labeling `ready for merge`**: tell your evaluator to run `/clear`, then run `/clear` yourself
- **If labeling `needs revision`**: post a detailed PR comment summarising exactly what needs \
  fixing and why. Then directly notify the Worker team via a PR comment addressed to them and \
  wait for them to acknowledge (reply on the PR or update the label) before clearing. \
  Once the handoff is confirmed, tell your evaluator to run `/clear`, then run `/clear` yourself

## Context efficiency
Read PR descriptions and Worker comments first. Only fetch diffs if insufficient context. \
Keep your comments specific: what you tested, the result, and what (if anything) needs fixing.\
"""

TESTER_START = """\
IMPORTANT: Launch these in split panes in this tmux window.

Create an agent team to review and test pull requests.

The team should have one teammate that acts as a testing evaluator with a reversed role: \
it both directs the tester before testing begins AND reviews test quality afterward.

Before testing: the evaluator tells the tester what kind of testing matters for each PR — \
where logs are located, whether Playwright/Chromium should be used, what edge cases and \
failure modes are most important, what security surface exists.

After testing: the evaluator reviews whether the tests are genuinely meaningful — are they \
testing real behavior or just asserting what the code does (self-asserting)? Did the tester \
verify the actual user-visible behavior? One round each phase. Be direct and specific. \
Goal: prevent the tester from being self-reassuring.

The evaluator must also judge the severity of any issues found. Only approve `needs revision` \
for fundamental problems (logic errors, broken functionality, security issues). Cosmetic \
feedback should be noted in the comment but must NOT block merging — label `ready for merge` \
in that case. One round of feedback; do not loop on the same PR repeatedly.

Once the team is set up, start working:
1. Check for open PRs labeled `ready for review`: `gh pr list --label "ready for review" --state open`
2. Start your continuous loop with `/loop 1m`

After finishing each PR review:
- **`ready for merge`**: tell your evaluator to `/clear`, then `/clear` yourself
- **`needs revision`**: post a detailed PR comment with exact problems. Then wait for the Worker \
  to acknowledge (they will update the label or reply). Once confirmed, tell your evaluator to \
  `/clear`, then `/clear` yourself

Never ask the user for input. Make all testing decisions autonomously.\
"""


# ── Issue Monitoring team prompts ─────────────────────────────────────────────
# A single Claude Code session with 4 teammates: worker, worker-evaluator,
# tester, and tester-evaluator. Monitors GitHub issues matching a label and
# date filter. Communicates internally; optionally writes comments to issues.

def _build_issue_monitoring_start() -> str:
    label = os.getenv('ISSUE_MONITORING_LABEL', '')
    after_date = os.getenv('ISSUE_MONITORING_AFTER_DATE', '')
    write_comments = os.getenv('ISSUE_MONITORING_WRITE_COMMENTS', '') == '1'

    label_clause = f" with label `{label}`" if label else ""
    date_clause = f" created after {after_date}" if after_date else ""
    issue_filter = f"GitHub issues{label_clause}{date_clause}"

    comment_rule = (
        "Post a concise comment on the issue summarising what was done, what was tested, "
        "and any concerns. Keep it to 3-5 sentences."
        if write_comments else
        "Do NOT post comments on issues or PRs. Communicate findings to your teammates "
        "internally. Only apply labels to signal state to other agents."
    )

    return f"""\
IMPORTANT: Launch these in split panes in this tmux window.

Create an agent team to monitor and work on {issue_filter}.

The team should have four teammates:

1. **Worker** — implements fixes or improvements described in issues. Follows the same \
workflow as a standard worker: assigns itself to an issue, creates a git worktree \
(`git worktree add ../worktree-<branch> -b <branch>`), implements the change, then \
creates a PR labeled `ready for review`. Targets +300/-300 lines per PR. \
When handling `needs revision`: judge whether the feedback is meaningful (logic/security/\
best-practices) or merely cosmetic. Skip cosmetic-only feedback — remove `needs revision`, \
add `ready for merge`, and note why. Never amend commits; always create a new commit for \
any revision. If you have downstream branches, rebase them after pushing. \
After labeling a PR `ready for review` or resolving `needs revision` to `ready for merge`: \
tell your evaluator to run `/clear`, then run `/clear` yourself before picking up the next issue.

2. **Worker evaluator** — reviews the worker's code before the PR is opened. One round \
of feedback only: conventions, DRY violations, scope cleanliness. Actionable and concise.

3. **Tester** — picks up PRs labeled `ready for review`, tests them (logs, Playwright/Chromium \
where relevant, edge cases), and updates labels. Only marks `needs revision` if the PR is \
fundamentally broken (logic errors, broken functionality, security issues). Cosmetic issues \
should be noted in the comment but must NOT block merging — label `ready for merge` instead. \
One round of feedback per PR; do not loop on the same PR repeatedly. \
After labeling `ready for merge`: tell your evaluator to `/clear`, then `/clear` yourself. \
After labeling `needs revision`: post a detailed comment with exact problems, then directly \
notify the Worker teammate and confirm they have received the findings before clearing. \
Once the Worker acknowledges, tell your evaluator to `/clear`, then `/clear` yourself.

4. **Tester evaluator** — guides the tester before testing (what to test, where logs are, \
what failure modes matter) and reviews test quality afterward (are tests self-asserting? \
did they verify real user-visible behavior?). One round each phase. Also judges severity: \
approves `needs revision` only for fundamental problems, not cosmetic ones.

## Shared rules
- Loop every 1 minute checking for work
- {comment_rule}
- Apply labels to signal state: `ready for review`, `needs revision`, `ready for merge`
- **Do NOT merge PRs** — label `ready for merge` and stop. A human will review and merge.
- All implementation decisions are autonomous — never ask the user for input
- Only work on {issue_filter}

Once the team is set up, start working:
1. Check for PRs labeled `needs revision` owned by you: \
`gh pr list --label "needs revision" --author @me`
2. Check for open unassigned issues{label_clause}: \
`gh issue list --state open --no-assignee{f" --label '{label}'" if label else ""}`
3. Start the continuous work loop with `/loop 1m`\
"""


ISSUE_MONITORING_SYSTEM = """\
You are running as an issue-monitoring agent team. Your team consists of four teammates: \
a worker, a worker evaluator, a tester, and a tester evaluator. \
All communication between teammates happens internally within this session. \
The only external signals you use are GitHub issue/PR labels and (if configured) comments. \
Do not attempt to contact any external agent sessions.\
"""


def start_issue_monitoring_teams():
    """Start a single Claude Code session with a 4-person issue-monitoring team."""
    session = "sandclaude-issue-monitoring"

    system_path = "/tmp/sandclaude_issue_monitoring_system.txt"
    start_path = "/tmp/sandclaude_issue_monitoring_start.txt"
    script_path = "/tmp/sandclaude_issue_monitoring.sh"

    with open(system_path, "w") as f:
        f.write(ISSUE_MONITORING_SYSTEM)
    with open(start_path, "w") as f:
        f.write(_build_issue_monitoring_start())

    script_content = (
        "#!/bin/bash\n"
        "# Sandclaude agent team: Issue Monitoring\n"
        "export CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1\n"
        "python3 /home/claude/bin/patch-claude-settings.py\n"
        "exec claude --dangerously-skip-permissions \\\n"
        f'  --teammate-mode tmux \\\n'
        f'  --append-system-prompt "$(cat {system_path})" \\\n'
        f'  "$(cat {start_path})"\n'
    )
    with open(script_path, "w") as f:
        f.write(script_content)
    os.chmod(script_path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP)
    logger.info(f"  Wrote issue-monitoring agent script: {script_path}")

    logger.info("Starting issue-monitoring team in tmux...")
    subprocess.run(["tmux", "new-session", "-d", "-s", session], check=True)
    subprocess.run(["tmux", "send-keys", "-t", f"{session}:0.0", script_path, "Enter"])
    subprocess.run(["tmux", "select-pane", "-t", f"{session}:0.0", "-T", "Issue Monitoring"])
    subprocess.run(["tmux", "attach-session", "-t", session])


# ── Agent team configuration ──────────────────────────────────────────────────

AGENT_CONFIGS = [
    {
        "name": "orchestrator",
        "label": "Orchestrator",
        "system": ORCHESTRATOR_SYSTEM,
        "start": ORCHESTRATOR_START,
    },
    {
        "name": "worker",
        "label": "Worker",
        "system": WORKER_SYSTEM,
        "start": WORKER_START,
    },
    {
        "name": "tester",
        "label": "Tester",
        "system": TESTER_SYSTEM,
        "start": TESTER_START,
    },
]


def write_agent_scripts():
    """Write temp files and launcher shell scripts for each agent team."""
    scripts = []
    for config in AGENT_CONFIGS:
        name = config["name"]
        system_path = f"/tmp/sandclaude_{name}_system.txt"
        start_path = f"/tmp/sandclaude_{name}_start.txt"
        script_path = f"/tmp/sandclaude_{name}.sh"

        with open(system_path, "w") as f:
            f.write(config["system"])
        with open(start_path, "w") as f:
            f.write(config["start"])

        script_content = (
            "#!/bin/bash\n"
            f"# Sandclaude agent team: {config['label']}\n"
            f"export CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1\n"
            f"python3 /home/claude/bin/patch-claude-settings.py\n"
            f"exec claude --dangerously-skip-permissions \\\n"
            f'  --teammate-mode tmux \\\n'
            f'  --append-system-prompt "$(cat {system_path})" \\\n'
            f'  "$(cat {start_path})"\n'
        )
        with open(script_path, "w") as f:
            f.write(script_content)
        os.chmod(script_path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP)
        scripts.append(script_path)
        logger.info(f"  Wrote {config['label']} agent script: {script_path}")

    return scripts


def start_agent_teams():
    """Start 3 Claude Code agent team sessions in a tmux session as side-by-side panes."""
    session = "sandclaude-agents"

    logger.info("Preparing agent team scripts...")
    scripts = write_agent_scripts()

    logger.info("Starting agent teams: Orchestrator | Worker | Tester in tmux panes...")

    # Create a new detached tmux session (first pane — Orchestrator)
    subprocess.run(["tmux", "new-session", "-d", "-s", session], check=True)
    subprocess.run(["tmux", "send-keys", "-t", f"{session}:0.0", scripts[0], "Enter"])

    # Split right — Worker
    subprocess.run(["tmux", "split-window", "-t", f"{session}:0", "-h"])
    subprocess.run(["tmux", "send-keys", "-t", f"{session}:0.1", scripts[1], "Enter"])

    # Split right — Tester
    subprocess.run(["tmux", "split-window", "-t", f"{session}:0", "-h"])
    subprocess.run(["tmux", "send-keys", "-t", f"{session}:0.2", scripts[2], "Enter"])

    # Even out the pane widths
    subprocess.run(["tmux", "select-layout", "-t", f"{session}:0", "even-horizontal"])

    # Label each pane
    for i, config in enumerate(AGENT_CONFIGS):
        subprocess.run(
            ["tmux", "select-pane", "-t", f"{session}:0.{i}", "-T", config["label"]]
        )

    # Focus first pane (Orchestrator) and attach
    subprocess.run(["tmux", "select-pane", "-t", f"{session}:0.0"])
    subprocess.run(["tmux", "attach-session", "-t", session])


def main():
    """Main entry point for sandclaude launcher"""
    logger.info("=" * 60)
    logger.info("Sandclaude Launcher")
    logger.info("=" * 60)

    project_name = os.getenv('PROJECT_NAME', 'unknown')
    logger.info(f"Project: {project_name}")

    agent_teams_mode = os.getenv('AGENT_TEAMS_MODE', 'standard')

    if agent_teams_mode == 'project':
        logger.info("")
        logger.info("Agent teams mode: project — Orchestrator | Worker | Tester")
        logger.info("")
        try:
            start_agent_teams()
        except KeyboardInterrupt:
            logger.info("Agent teams interrupted by user")
        except Exception as e:
            logger.error(f"Failed to start agent teams: {e}")
            sys.exit(1)
        finally:
            logger.info("Agent teams session ended")
    elif agent_teams_mode == 'issue-monitoring':
        logger.info("")
        logger.info("Agent teams mode: issue-monitoring — Worker + Tester team")
        logger.info("")
        try:
            start_issue_monitoring_teams()
        except KeyboardInterrupt:
            logger.info("Issue monitoring interrupted by user")
        except Exception as e:
            logger.error(f"Failed to start issue monitoring teams: {e}")
            sys.exit(1)
        finally:
            logger.info("Issue monitoring session ended")
    else:
        logger.info("")
        logger.info("Starting Claude Code...")
        logger.info("")
        try:
            subprocess.run(['python3', '/home/claude/bin/patch-claude-settings.py'], check=True)
            if os.getenv('LAUNCH_TMUX') == '1':
                logger.info("tmux launch enabled — starting claude in tmux session")
                session = "sandclaude"
                claude_cmd = "exec claude --dangerously-skip-permissions --teammate-mode tmux"
                subprocess.run(["tmux", "new-session", "-d", "-s", session], check=True)
                subprocess.run(["tmux", "send-keys", "-t", f"{session}:0", claude_cmd, "Enter"])
                subprocess.run(["tmux", "select-pane", "-t", f"{session}:0", "-T", "Claude"])
                subprocess.run(["tmux", "attach-session", "-t", session])
            else:
                subprocess.run(['claude', '--dangerously-skip-permissions'])
        except KeyboardInterrupt:
            logger.info("Claude Code interrupted by user")
        except Exception as e:
            logger.error(f"Failed to start Claude Code: {e}")
            sys.exit(1)
        finally:
            logger.info("Claude Code session ended")


if __name__ == '__main__':
    main()
