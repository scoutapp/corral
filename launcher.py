#!/usr/bin/env -S uv run --quiet --script
"""
Sandclaude Launcher - Starts Claude Code
"""

import json
import logging
import os
import random
import shlex
import subprocess
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S'
)
logger = logging.getLogger(__name__)


class GitHubIssueMonitor:
    """Monitors GitHub issues, assigns them to the current user, and works on them."""

    def __init__(self):
        self.repo = os.getenv('GITHUB_REPO', '')
        self.labels = os.getenv('GITHUB_ISSUE_LABELS', '')
        self.since = os.getenv('GITHUB_ISSUE_SINCE', '')  # stored as MM-DD-YYYY
        self._stop_event = threading.Event()

        log_dir = Path('/home/claude/logs')
        log_dir.mkdir(exist_ok=True)

        self.gh_logger = logging.getLogger('github_issues')
        self.gh_logger.setLevel(logging.INFO)
        self.gh_logger.propagate = False
        handler = logging.FileHandler(str(log_dir / 'issue-monitoring.log'))
        handler.setFormatter(logging.Formatter(
            '%(asctime)s [%(levelname)s] %(message)s',
            datefmt='%Y-%m-%d %H:%M:%S'
        ))
        self.gh_logger.addHandler(handler)

    def _since_iso(self) -> str:
        """Convert stored MM-DD-YYYY to YYYY-MM-DD for GitHub search syntax."""
        if not self.since:
            return ''
        try:
            return datetime.strptime(self.since, '%m-%d-%Y').strftime('%Y-%m-%d')
        except ValueError:
            return ''

    def _get_current_user(self) -> str:
        try:
            result = subprocess.run(
                ['gh', 'api', 'user', '--jq', '.login'],
                capture_output=True, text=True, timeout=10
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception as e:
            self.gh_logger.error(f"Failed to get current GitHub user: {e}")
        return ''

    def _get_unassigned_issues(self) -> list:
        search_terms = ['no:assignee', 'is:open', 'is:issue']
        since_iso = self._since_iso()
        if since_iso:
            search_terms.append(f'created:>={since_iso}')

        cmd = [
            'gh', 'issue', 'list',
            '--repo', self.repo,
            '--state', 'open',
            '--search', ' '.join(search_terms),
            '--json', 'number,title,body,url,labels,assignees',
            '--limit', '20',
        ]

        if self.labels:
            for label in self.labels.split(','):
                label = label.strip()
                if label:
                    cmd.extend(['--label', label])

        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
            if result.returncode == 0:
                issues = json.loads(result.stdout)
                # Double-check assignees field is empty
                return [i for i in issues if not i.get('assignees')]
            else:
                self.gh_logger.error(f"gh issue list failed: {result.stderr.strip()}")
        except Exception as e:
            self.gh_logger.error(f"Failed to fetch issues: {e}")
        return []

    def _assign_issue(self, issue_number: int) -> bool:
        try:
            result = subprocess.run(
                ['gh', 'issue', 'edit', str(issue_number),
                 '--repo', self.repo,
                 '--add-assignee', '@me'],
                capture_output=True, text=True, timeout=10
            )
            return result.returncode == 0
        except Exception as e:
            self.gh_logger.error(f"Failed to assign issue #{issue_number}: {e}")
            return False

    def _is_still_assigned_to_me(self, issue_number: int, current_user: str) -> bool:
        try:
            result = subprocess.run(
                ['gh', 'issue', 'view', str(issue_number),
                 '--repo', self.repo,
                 '--json', 'assignees'],
                capture_output=True, text=True, timeout=10
            )
            if result.returncode == 0:
                data = json.loads(result.stdout)
                assignees = [a['login'] for a in data.get('assignees', [])]
                return current_user in assignees
        except Exception as e:
            self.gh_logger.error(f"Failed to check assignment for #{issue_number}: {e}")
        return False

    def _get_issue_sessions(self) -> list[str]:
        """Return issue tmux session names sorted oldest-first by creation time."""
        try:
            result = subprocess.run(
                ['tmux', 'list-sessions', '-F', '#{session_created} #{session_name}'],
                capture_output=True, text=True, timeout=5
            )
            if result.returncode != 0:
                return []
            sessions = []
            for line in result.stdout.strip().splitlines():
                parts = line.split(' ', 1)
                if len(parts) == 2 and parts[1].startswith('claude-issue-'):
                    sessions.append((int(parts[0]), parts[1]))
            sessions.sort()
            return [name for _, name in sessions]
        except Exception as e:
            self.gh_logger.error(f"Failed to list tmux sessions: {e}")
            return []

    def _prune_old_sessions(self):
        """Kill oldest issue sessions so there are at most 9 before adding a new one."""
        sessions = self._get_issue_sessions()
        while len(sessions) >= 10:
            oldest = sessions.pop(0)
            self.gh_logger.info(f"Pruning old tmux session: {oldest}")
            subprocess.run(['tmux', 'kill-session', '-t', oldest],
                           capture_output=True, timeout=5)

    def _work_on_issue(self, issue: dict):
        number = issue['number']
        title = issue['title']
        body = issue.get('body') or ''
        url = issue['url']
        labels = ', '.join(l['name'] for l in issue.get('labels', []))

        self.gh_logger.info(f"Starting work on issue #{number}: {title}")

        prompt = (
            f"Please work on GitHub issue #{number}: {title}\n\n"
            f"Issue URL: {url}\n"
        )
        if labels:
            prompt += f"Labels: {labels}\n"
        if body:
            prompt += f"\nDescription:\n{body}\n"
        prompt += "\nInvestigate the issue, make the necessary changes, and resolve it."

        session_name = f'claude-issue-{number}'
        prompt_file = f'/tmp/claude-issue-{number}.txt'

        self._prune_old_sessions()

        try:
            Path(prompt_file).write_text(prompt)

            # Create a new tmux session and run claude in it
            bash_cmd = f'claude --dangerously-skip-permissions -p "$(cat {shlex.quote(prompt_file)})"'
            subprocess.run(
                ['tmux', 'new-session', '-d', '-s', session_name, 'bash', '-c', bash_cmd],
                timeout=10
            )
            self.gh_logger.info(f"Issue #{number} running in tmux session '{session_name}'")

            # Wait for the session to exit (up to 1 hour)
            deadline = time.monotonic() + 3600
            while time.monotonic() < deadline:
                if self._stop_event.wait(timeout=10):
                    break
                result = subprocess.run(
                    ['tmux', 'has-session', '-t', session_name],
                    capture_output=True, timeout=5
                )
                if result.returncode != 0:
                    break  # Session exited

            self.gh_logger.info(f"Completed work on issue #{number}")
        except Exception as e:
            self.gh_logger.error(f"Error working on issue #{number}: {e}")
        finally:
            try:
                Path(prompt_file).unlink(missing_ok=True)
            except Exception:
                pass

    def run(self):
        self.gh_logger.info(f"GitHub issue monitor started — repo: {self.repo}")
        if self.labels:
            self.gh_logger.info(f"  Labels filter: {self.labels}")
        if self.since:
            self.gh_logger.info(f"  Since: {self.since}")

        current_user = self._get_current_user()
        if not current_user:
            self.gh_logger.error("Could not determine current GitHub user — monitor stopping")
            return

        self.gh_logger.info(f"Authenticated as: {current_user}")

        poll_count = 0
        while not self._stop_event.is_set():
            poll_count += 1
            try:
                self.gh_logger.info(f"Polling for unassigned issues (poll #{poll_count})...")
                issues = self._get_unassigned_issues()
                if issues:
                    issue = issues[0]
                    number = issue['number']
                    self.gh_logger.info(
                        f"Found unassigned issue #{number}: {issue['title']}"
                    )

                    if self._assign_issue(number):
                        self.gh_logger.info(f"Assigned issue #{number} to @{current_user}")

                        # Wait 2-10 seconds to verify assignment (in case of race condition
                        # with other sandclaude instances or manual assignment)
                        wait = random.uniform(2, 10)
                        self.gh_logger.info(
                            f"Waiting {wait:.1f}s then verifying assignment..."
                        )
                        self._stop_event.wait(timeout=wait)

                        if self._stop_event.is_set():
                            break

                        if self._is_still_assigned_to_me(number, current_user):
                            self.gh_logger.info(
                                f"Still assigned to #{number}, beginning work"
                            )
                            self._work_on_issue(issue)
                            # After completing work, immediately check for next issue
                            continue
                        else:
                            self.gh_logger.info(
                                f"Issue #{number} reassigned — skipping"
                            )
                    else:
                        self.gh_logger.warning(
                            f"Failed to assign issue #{number} — will retry next poll"
                        )
                else:
                    self.gh_logger.info("No unassigned issues found — waiting 60s before next poll")

            except Exception as e:
                self.gh_logger.error(f"Monitor loop error: {e}")

            # Poll every 60 seconds for new issues (only when no issues found or after errors)
            self._stop_event.wait(timeout=60)

        self.gh_logger.info("GitHub issue monitor stopped")

    def stop(self):
        self._stop_event.set()


def main():
    """Main entry point for sandclaude launcher"""
    logger.info("=" * 60)
    logger.info("Sandclaude Launcher")
    logger.info("=" * 60)

    project_name = os.getenv('PROJECT_NAME', 'unknown')
    logger.info(f"Project: {project_name}")

    # Start GitHub issue monitor in background if configured
    monitor = None
    github_repo = os.getenv('GITHUB_REPO', '')
    if github_repo:
        logger.info(f"GitHub issue monitoring enabled (repo: {github_repo})")
        logger.info("  Monitor logs: /home/claude/logs/issue-monitoring.log")
        monitor = GitHubIssueMonitor()
        monitor_thread = threading.Thread(target=monitor.run, daemon=True)
        monitor_thread.start()
    else:
        logger.info("GitHub issue monitoring not configured")

    logger.info("")

    # Check for existing tmux sessions and display them
    try:
        result = subprocess.run(
            ['tmux', 'list-sessions', '-F', '#{session_name}'],
            capture_output=True, text=True, timeout=5
        )
        if result.returncode == 0 and result.stdout.strip():
            sessions = result.stdout.strip().splitlines()
            logger.info("Existing tmux sessions detected:")
            for session in sessions:
                logger.info(f"  • {session}")
            logger.info("")
            logger.info("To view all sessions: tmux ls")
            logger.info("To attach to a session: tmux attach -t <session-name>")
            logger.info("Inside tmux:")
            logger.info("  • Ctrl+b d - Detach from session")
            logger.info("  • Ctrl+b s - Switch sessions interactively")
            logger.info("")
    except Exception:
        pass

    # Create a persistent home/volume shell session
    try:
        result = subprocess.run(
            ['tmux', 'has-session', '-t', 'claude-home'],
            capture_output=True, timeout=5
        )
        if result.returncode != 0:
            subprocess.run(
                ['tmux', 'new-session', '-d', '-s', 'claude-home', '-c', '/home/claude', '/bin/bash'],
                timeout=10
            )
            logger.info("Created tmux session 'claude-home' (home/volume shell)")
    except Exception as e:
        logger.warning(f"Could not create claude-home session: {e}")

    logger.info("Starting Claude Code in tmux session 'claude-main'...")
    logger.info("")
    logger.info("TIP: After detaching (Ctrl+b d), run 'tmux ls' to see all sessions")
    logger.info("")

    try:
        # Create session and wrap with a shell that shows sessions on exit
        bash_cmd = '''
claude --dangerously-skip-permissions
echo ""
echo "=== Claude Code session ended ==="
echo "Active tmux sessions:"
tmux ls 2>/dev/null || echo "  (none)"
echo ""
echo "To reattach: tmux attach -t claude-main"
echo "To view issue sessions: tmux attach -t claude-issue-<number>"
echo ""
read -p "Press Enter to exit..."
'''
        if monitor:
            # Monitor thread must stay alive; use subprocess so Python keeps running.
            subprocess.run(
                ['tmux', 'new-session', '-s', 'claude-main', 'bash', '-c', bash_cmd],
                check=True
            )
        else:
            # Replace Python process with tmux so it owns the PTY directly.
            # This is required for tmux prefix keys (e.g. Ctrl-a) to work.
            os.execvp('tmux', ['tmux', 'new-session', '-s', 'claude-main', 'bash', '-c', bash_cmd])
    except KeyboardInterrupt:
        logger.info("Claude Code interrupted by user")
    except Exception as e:
        logger.error(f"Failed to start Claude Code: {e}")
        sys.exit(1)
    finally:
        if monitor:
            monitor.stop()
        logger.info("Claude Code session ended")


if __name__ == '__main__':
    main()
