#!/usr/bin/env -S uv run --quiet --script
"""
Sandclaude Launcher - Starts Claude Code
"""

import os
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


def main():
    """Main entry point for sandclaude launcher"""
    logger.info("=" * 60)
    logger.info("Sandclaude Launcher")
    logger.info("=" * 60)

    project_name = os.getenv('PROJECT_NAME', 'unknown')
    logger.info(f"Project: {project_name}")

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
