#!/usr/bin/env -S uv run --quiet --script
"""
Sandclaude Launcher - Starts Claude Code with GitHub issue monitoring

This launcher:
1. Uses GitHub CLI for authentication (mounted from host)
2. Monitors GitHub for new issues every minute
3. Starts Claude Code sessions to work on issues
4. Never exposes credentials outside the container

Uses `gh` CLI which is already authenticated via mounted ~/.config/gh
"""

import os
import sys
import time
import json
import logging
import subprocess
from pathlib import Path
from datetime import datetime, timedelta
from typing import Optional, Dict, List, Set
import threading

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S'
)
logger = logging.getLogger(__name__)


class GitHubClient:
    """GitHub client using gh CLI (already authenticated via host mount)"""

    def __init__(self):
        self.repo = os.getenv('GITHUB_REPO', '')
        self._check_gh_cli()

    def _check_gh_cli(self):
        """Check if gh CLI is available and authenticated"""
        try:
            result = subprocess.run(
                ['gh', 'auth', 'status'],
                capture_output=True,
                text=True,
                timeout=5
            )
            if result.returncode == 0:
                logger.info("GitHub CLI authenticated successfully")
            else:
                logger.warning("GitHub CLI not authenticated")
        except FileNotFoundError:
            logger.warning("gh CLI not found in PATH")
        except Exception as e:
            logger.warning(f"Failed to check gh CLI status: {e}")

    def is_configured(self) -> bool:
        """Check if GitHub integration is configured"""
        if not self.repo:
            return False

        try:
            subprocess.run(
                ['gh', 'auth', 'status'],
                capture_output=True,
                check=True,
                timeout=5
            )
            return True
        except:
            return False

    def get_unassigned_issues(self, limit: int = 10) -> List[Dict]:
        """Get recent unassigned issues from GitHub"""
        if not self.repo:
            return []

        try:
            cmd = [
                'gh', 'issue', 'list',
                '--repo', self.repo,
                '--state', 'open',
                '--json', 'number,title,body,url,createdAt,labels',
                '--limit', str(limit)
            ]

            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                check=True,
                timeout=30
            )

            issues = json.loads(result.stdout)
            # Filter for unassigned (no assignee)
            cmd_detailed = [
                'gh', 'issue', 'list',
                '--repo', self.repo,
                '--state', 'open',
                '--assignee', '@me',
                '--json', 'number',
                '--limit', '100'
            ]
            assigned_result = subprocess.run(
                cmd_detailed,
                capture_output=True,
                text=True,
                timeout=30
            )
            assigned_numbers = {i['number'] for i in json.loads(assigned_result.stdout)} if assigned_result.returncode == 0 else set()

            # Return issues not assigned to anyone
            unassigned = [i for i in issues if i['number'] not in assigned_numbers]
            return unassigned[:limit]

        except subprocess.CalledProcessError as e:
            logger.error(f"Failed to fetch GitHub issues: {e.stderr}")
            return []
        except Exception as e:
            logger.error(f"Error fetching GitHub issues: {e}")
            return []

    def assign_issue_to_me(self, issue_number: int) -> bool:
        """Assign an issue to the current user"""
        if not self.repo:
            return False

        try:
            cmd = [
                'gh', 'issue', 'edit', str(issue_number),
                '--repo', self.repo,
                '--add-assignee', '@me'
            ]

            subprocess.run(
                cmd,
                capture_output=True,
                check=True,
                timeout=10
            )
            return True
        except Exception as e:
            logger.error(f"Failed to assign issue #{issue_number}: {e}")
            return False

    def add_comment(self, issue_number: int, comment: str) -> bool:
        """Add a comment to an issue"""
        if not self.repo:
            return False

        try:
            cmd = [
                'gh', 'issue', 'comment', str(issue_number),
                '--repo', self.repo,
                '--body', comment
            ]

            subprocess.run(
                cmd,
                capture_output=True,
                check=True,
                timeout=10
            )
            return True
        except Exception as e:
            logger.error(f"Failed to add comment to issue #{issue_number}: {e}")
            return False


class ClaudeLauncher:
    """Manages Claude Code sessions with GitHub integration"""

    def __init__(self):
        self.github = GitHubClient()
        self.processed_issues: Set[int] = set()
        self.state_file = Path.home() / '.sandclaude' / 'processed_issues.json'
        self.state_file.parent.mkdir(parents=True, exist_ok=True)
        self._load_state()

    def _load_state(self):
        """Load previously processed issues from disk"""
        if self.state_file.exists():
            try:
                with open(self.state_file, 'r') as f:
                    data = json.load(f)
                    self.processed_issues = set(data.get('processed', []))
                logger.info(f"Loaded {len(self.processed_issues)} processed issues from state")
            except Exception as e:
                logger.warning(f"Failed to load state: {e}")

    def _save_state(self):
        """Save processed issues to disk"""
        try:
            with open(self.state_file, 'w') as f:
                json.dump({'processed': list(self.processed_issues)}, f)
        except Exception as e:
            logger.warning(f"Failed to save state: {e}")

    def launch_claude_for_issue(self, issue: Dict):
        """Launch Claude Code to work on a specific issue"""
        issue_number = issue['number']
        title = issue['title']
        description = issue.get('body', '')
        url = issue['url']

        logger.info(f"Starting work on #{issue_number}: {title}")

        # Prepare prompt for Claude
        prompt = f"""You are working on GitHub issue #{issue_number}.

**Title:** {title}

**Description:**
{description or '(No description provided)'}

**Issue URL:** {url}

Please analyze this issue and:
1. Understand the requirements
2. Review relevant code if needed
3. Implement the necessary changes
4. Test your changes
5. Report back with what you've done

Work autonomously but ask for clarification if the requirements are unclear.
"""

        # Write prompt to temporary file
        prompt_file = Path('/tmp') / f'issue_{issue_number}.txt'
        prompt_file.write_text(prompt)

        logger.info(f"Prompt written to {prompt_file}")
        logger.info(f"To work on this issue, Claude will load the prompt and start")

        # Mark as processed
        self.processed_issues.add(issue_number)
        self._save_state()

        # Add comment to GitHub
        if self.github.is_configured():
            comment = f"🤖 Sandclaude started working on this issue at {datetime.utcnow().isoformat()}Z"
            self.github.add_comment(issue_number, comment)

        return prompt_file

    def check_for_new_issues(self):
        """Check GitHub for new unassigned issues"""
        if not self.github.is_configured():
            logger.debug("GitHub not configured, skipping issue check")
            return

        logger.info("Checking GitHub for new issues...")

        try:
            issues = self.github.get_unassigned_issues(limit=10)

            if not issues:
                logger.debug("No unassigned issues found")
                return

            new_issues = [
                issue for issue in issues
                if issue['number'] not in self.processed_issues
            ]

            if not new_issues:
                logger.debug(f"Found {len(issues)} issues but all already processed")
                return

            logger.info(f"Found {len(new_issues)} new issues")

            for issue in new_issues:
                issue_number = issue['number']
                title = issue['title']

                logger.info(f"New issue: #{issue_number} - {title}")

                # Assign to bot
                if self.github.is_configured():
                    self.github.assign_issue_to_me(issue_number)

                # Prepare work on the issue
                prompt_file = self.launch_claude_for_issue(issue)

                logger.info(f"Issue #{issue_number} ready. Prompt at: {prompt_file}")
                logger.info(f"Run: claude --prompt {prompt_file}")

        except Exception as e:
            logger.error(f"Error checking for issues: {e}", exc_info=True)

    def run_monitor(self, interval: int = 60):
        """Run continuous monitoring loop"""
        logger.info(f"Starting GitHub issue monitor (interval: {interval}s)")

        while True:
            try:
                self.check_for_new_issues()
            except KeyboardInterrupt:
                logger.info("Monitor stopped by user")
                break
            except Exception as e:
                logger.error(f"Monitor error: {e}", exc_info=True)

            time.sleep(interval)


def main():
    """Main entry point for sandclaude launcher"""
    logger.info("=" * 60)
    logger.info("Sandclaude Launcher")
    logger.info("=" * 60)

    project_name = os.getenv('PROJECT_NAME', 'unknown')
    logger.info(f"Project: {project_name}")

    launcher = ClaudeLauncher()

    # Check if GitHub is configured
    if launcher.github.is_configured():
        logger.info("✅ GitHub integration configured")
        logger.info(f"   Repository: {launcher.github.repo}")

        # Start monitoring thread
        monitor_thread = threading.Thread(
            target=launcher.run_monitor,
            args=(60,),  # Check every 60 seconds
            daemon=True
        )
        monitor_thread.start()
        logger.info("📊 GitHub issue monitor started (checking every 60s)")
    else:
        logger.info("⚠️  GitHub integration not configured")
        logger.info("   To enable: sandclaude init <project> and provide GitHub repo")

    logger.info("")
    logger.info("Starting Claude Code...")
    logger.info("")

    # Launch Claude Code (will block here)
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
