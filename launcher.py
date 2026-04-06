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

## PR size and scope
- Target +300/-300 lines per PR; larger only when splitting would break functionality
- Split by domain (what it accomplishes for the user) — not by file type
  - Right: "auth flow" vs "profile management"
  - Wrong: "models" vs "controllers" for the same feature
- Use trunk-based development; use feature flags to isolate incomplete features in main
- If PRs are inseparably linked, note it: "Part 1 of 2 — see #456"
- PR descriptions: what problem, what approach, any linked PRs — specific but not verbose

## Context efficiency
Read PR comments and descriptions when resuming work, not full diffs. \
Keep your own comments concise but specific enough to rebuild context later.\
"""

WORKER_START = """\
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
   - Issues found: `needs revision` — list specific problems in the comment
   - All good: `ready for merge`
4. If the PR is part of a chain (linked PRs in description), read all linked PR descriptions \
   before testing and evaluate the chain as a whole

## Context efficiency
Read PR descriptions and Worker comments first. Only fetch diffs if insufficient context. \
Keep your comments specific: what you tested, the result, and what (if anything) needs fixing.\
"""

TESTER_START = """\
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

Once the team is set up, start working:
1. Check for open PRs labeled `ready for review`: `gh pr list --label "ready for review" --state open`
2. Start your continuous loop with `/loop 1m`

Never ask the user for input. Make all testing decisions autonomously.\
"""


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
            f"exec claude --dangerously-skip-permissions \\\n"
            f'  --teammate-mode in-process \\\n'
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

    agent_teams = os.getenv('AGENT_TEAMS_ENABLED', '') == '1'

    if agent_teams:
        logger.info("")
        logger.info("Agent teams mode: Orchestrator | Worker | Tester")
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
    else:
        logger.info("")
        logger.info("Starting Claude Code...")
        logger.info("")
        try:
            claude_cmd = ['claude', '--dangerously-skip-permissions']
            subprocess.run(claude_cmd)
        except KeyboardInterrupt:
            logger.info("Claude Code interrupted by user")
        except Exception as e:
            logger.error(f"Failed to start Claude Code: {e}")
            sys.exit(1)
        finally:
            logger.info("Claude Code session ended")


if __name__ == '__main__':
    main()
