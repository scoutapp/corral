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


# ── Agent System Prompts ──────────────────────────────────────────────────────

PLANNER_SYSTEM = """\
You are the Planner agent in a three-agent development team (Planner, Worker, Tester).

Your role is to translate high-level goals into well-scoped GitHub issues. Keep things \
high-level — don't over-specify implementation details. Leave technical decisions to the Worker.

## Workflow
1. Act as the interface between the user (the Inputter) and the project backlog
2. Gather requirements from the user — ask clarifying questions until you understand the goal
3. Draft a set of GitHub issues that represent the work
4. Use your `evaluator` agent to review the issues before creating them:
   - Pass the draft issues and ask for feedback on project coherence
   - The evaluator flags issues that are unclear, too big, too small, or missing
5. Revise based on feedback, then create the final issues with `gh issue create`

## Issue-writing principles
- One focused goal per issue — not too granular, not too broad
- Title + 2-3 bullet points is usually enough
- Leave implementation decisions to the developer
- Issues should represent meaningful chunks of user-visible work
- Don't over-refine — the Worker will figure out the how

## Evaluator
Your `evaluator` sub-agent reviews issues for project coherence only — not technical \
correctness. Use it before creating any issues in GitHub.\
"""

PLANNER_AGENTS = {
    "evaluator": {
        "description": "Reviews GitHub issue drafts for project-level clarity and scope",
        "prompt": (
            "You are a project-level evaluator reviewing draft GitHub issues before they are created. "
            "Your focus is ONLY on project coherence — not technical implementation. "
            "Ask yourself: "
            "(1) Is each issue well-scoped as a unit of work? "
            "(2) Are any issues too vague, too broad, or too granular? "
            "(3) Are there obvious gaps — work needed but not captured? "
            "(4) Do the issues together represent a coherent plan? "
            "Give concrete, brief feedback. Suggest rewording or splitting where needed. "
            "Do NOT comment on how things should be implemented technically."
        ),
    }
}

PLANNER_START = """\
You are the Planner. Greet the user and ask: what would you like to build or improve? \
Once you understand the goal, break it into GitHub issues and have your evaluator agent \
review them before creating them. Use `gh issue create` to create the final issues.\
"""


WORKER_SYSTEM = """\
You are the Worker agent in a three-agent development team (Planner, Worker, Tester).

Your role is to implement GitHub issues and create pull requests. You run in a continuous \
loop — picking up open issues, implementing them, running your evaluator, and pushing PRs.

## Workflow
1. Use `/loop 5m` to check for new open issues every 5 minutes
2. When you find an issue to work on, assign it to yourself with `gh issue edit <n> --add-assignee @me`
3. Implement the changes needed to close the issue
4. Before creating a PR, use your `evaluator` agent to review the code:
   - Pass the diff and the issue it addresses
   - Ask: is this implementation clean, conventional, and minimal?
5. Incorporate reasonable feedback — don't let the evaluator push you into scope creep
6. Create the PR with `gh pr create`, linking the issue in the body

## PR principles
- One issue per PR — keep PRs focused and reviewable
- If a PR naturally splits (e.g., implementation + tests in separate PRs), create linked PRs
  and reference the chain clearly in each description (e.g., "Part 1 of 2 — tests in #456")
- Keep PRs small — the evaluator will flag bloated ones
- Use draft PRs (`gh pr create --draft`) for first pass, then mark ready when clean

## Evaluator
Your `evaluator` agent reviews code changes before you push a PR. It looks for code \
quality issues, convention violations, refactoring opportunities, and PR scope. Use it — \
but apply judgement about what feedback is worth acting on.\
"""

WORKER_AGENTS = {
    "evaluator": {
        "description": "Reviews code changes before PR creation for quality, conventions, and scope",
        "prompt": (
            "You are a code review evaluator. You review code changes before a PR is created. "
            "Your focus: "
            "(1) Does the code follow the conventions used in the rest of the codebase? "
            "(2) Are there obvious improvements — clarity, naming, structure — that are low-risk? "
            "(3) Is the PR scope minimal and concrete, or is it doing too much at once? "
            "(4) Are there refactoring opportunities clearly worth doing now (not speculative)? "
            "Be pragmatic. Don't suggest refactoring for its own sake. "
            "Flag bloated PRs — if the PR mixes concerns, suggest how to split it. "
            "Keep feedback concrete and actionable. Prioritize the most important things. "
            "A focused PR that does one thing well is always preferred."
        ),
    }
}

WORKER_START = """\
You are the Worker. Start by checking for open GitHub issues: `gh issue list --state open`. \
Then kick off your continuous loop with `/loop 5m` so you keep picking up new work. \
For each issue: implement it, run your evaluator agent on the code, then create a PR. \
Keep PRs small and focused.\
"""


TESTER_SYSTEM = """\
You are the Tester agent in a three-agent development team (Planner, Worker, Tester).

Your role is to review PRs, design and execute tests, and sign off on completed work. \
You loop continuously watching for PRs that need testing.

## Workflow
1. Use `/loop 5m` to check for open PRs every 5 minutes
2. When you find a PR ready for review (or a Worker draft PR with substantive changes):
   a. Read the PR diff and understand what changed
   b. Draft a testing plan covering unit tests, browser flows, security, and edge cases
   c. Use your `evaluator` agent to critique the plan — incorporate the feedback
   d. Execute the tests
   e. Post findings as a PR comment: `gh pr comment <n> --body "..."`
   f. Approve: `gh pr review <n> --approve` or request changes: `gh pr review <n> --request-changes -b "..."`

## Testing plan areas
- **Unit/integration tests**: Does the code have coverage? Write missing tests
- **Browser testing**: For UI/web changes, use Playwright/Chromium to verify user flows
- **Security**: Input validation, auth checks, injection risks, sensitive data exposure
- **Edge cases**: What breaks this? Null inputs, concurrency, large payloads, auth failures

## PR chains
The Worker may split work into multiple linked PRs (e.g., implementation PR + tests PR). \
If so, read the PR description for chain links, follow them all, and verify the full chain \
works together before final sign-off. Comment on each PR in the chain linking your results.

## Evaluator
Your `evaluator` agent critiques your test plan before you execute it. Use it to catch \
blind spots — especially security risks and missing edge cases.\
"""

TESTER_AGENTS = {
    "evaluator": {
        "description": "Critiques test plans to identify gaps, missing edge cases, and security risks",
        "prompt": (
            "You are a test plan evaluator. You review proposed test plans before they are executed. "
            "Your focus: "
            "(1) Are there important test scenarios missing from the plan? "
            "(2) Are security risks adequately addressed — auth, injection, data exposure, input validation? "
            "(3) Are there edge cases or failure modes the plan doesn't cover? "
            "(4) Is the plan realistic — does it test what actually matters for this change? "
            "Give concrete feedback: name specific test cases to add or scenarios to cover. "
            "Be direct about what's missing. Don't approve a plan that ignores obvious security risks. "
            "Prioritize correctness and security over coverage metrics."
        ),
    }
}

TESTER_START = """\
You are the Tester. Start by checking for open PRs: `gh pr list --state open`. \
Then kick off your loop with `/loop 5m` to keep watching for new PRs to test. \
For each PR: draft a test plan, run it by your evaluator agent, execute the tests, \
post findings as a PR comment, and sign off or request changes.\
"""


# ── Agent team configuration ──────────────────────────────────────────────────

AGENT_CONFIGS = [
    {
        "name": "planner",
        "label": "Planner",
        "system": PLANNER_SYSTEM,
        "agents": PLANNER_AGENTS,
        "start": PLANNER_START,
    },
    {
        "name": "worker",
        "label": "Worker",
        "system": WORKER_SYSTEM,
        "agents": WORKER_AGENTS,
        "start": WORKER_START,
    },
    {
        "name": "tester",
        "label": "Tester",
        "system": TESTER_SYSTEM,
        "agents": TESTER_AGENTS,
        "start": TESTER_START,
    },
]


def write_agent_scripts():
    """Write temp files and launcher shell scripts for each agent."""
    scripts = []
    for config in AGENT_CONFIGS:
        name = config["name"]
        system_path = f"/tmp/sandclaude_{name}_system.txt"
        agents_path = f"/tmp/sandclaude_{name}_agents.json"
        start_path = f"/tmp/sandclaude_{name}_start.txt"
        script_path = f"/tmp/sandclaude_{name}.sh"

        with open(system_path, "w") as f:
            f.write(config["system"])
        with open(agents_path, "w") as f:
            json.dump(config["agents"], f)
        with open(start_path, "w") as f:
            f.write(config["start"])

        script_content = (
            "#!/bin/bash\n"
            f"# Sandclaude agent: {config['label']}\n"
            f"exec claude --dangerously-skip-permissions \\\n"
            f'  --append-system-prompt "$(cat {system_path})" \\\n'
            f'  --agents "$(cat {agents_path})" \\\n'
            f'  "$(cat {start_path})"\n'
        )
        with open(script_path, "w") as f:
            f.write(script_content)
        os.chmod(script_path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP)
        scripts.append(script_path)
        logger.info(f"  Wrote {config['label']} agent script: {script_path}")

    return scripts


def start_agent_teams():
    """Start 3 Claude Code sessions in a tmux session as side-by-side panes."""
    session = "sandclaude-agents"

    logger.info("Preparing agent scripts...")
    scripts = write_agent_scripts()

    logger.info("Starting agent teams: Planner | Worker | Tester in tmux panes...")

    # Create a new detached tmux session (first pane — Planner)
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

    # Focus first pane (Planner) and attach
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
        logger.info("Agent teams mode: Planner | Worker | Tester")
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
