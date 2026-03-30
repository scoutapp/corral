#!/bin/bash
# User entrypoint - Runs as claude user (non-privileged)
# Launches Claude Code and drops to bash shell after exit

# Launch Python launcher (which starts Claude Code + GitHub monitoring)
# Redirect its output to properly handle stdin/stdout/stderr
/home/claude/launcher.py "$@"

echo ""
echo "Claude Code exited. Dropping to bash shell..."
echo "Type 'exit' to close the container."
echo ""

exec bash
