#!/bin/bash
# Sandclaude entrypoint - Starts Python launcher with Linear monitoring
# After Claude exits, drop to an interactive bash shell

echo "🚀 Starting sandclaude with firewall protection..."
echo ""
echo "Firewall is active. Blocked connections can be approved via:"
echo "  firewall-helper.sh monitor"
echo ""
echo "To manage firewall rules:"
echo "  firewall-helper.sh list          - List allowed domains"
echo "  firewall-helper.sh add <domain>  - Add a domain"
echo "  firewall-helper.sh remove <domain> - Remove a domain"
echo ""

# Start firewall monitor in background if interactive mode is enabled
if [ -f "/home/claude/.firewall/interactive-mode" ] && [ "$(cat /home/claude/.firewall/interactive-mode)" = "enabled" ]; then
    echo "📊 Interactive firewall mode enabled"
    echo "   Run 'firewall-helper.sh monitor' to approve blocked connections"
    echo ""
fi

# Check if GitHub integration is configured
if [ -n "$GITHUB_REPO" ]; then
    echo "✅ GitHub integration configured"
    echo "   Repository: $GITHUB_REPO"
    echo "   Issue monitoring will start in background"
    echo ""
else
    echo "⚠️  GitHub integration not configured"
    echo "   Run 'sandclaude init <project>' to set up GitHub monitoring"
    echo ""
fi

# Launch Python launcher (which starts Claude Code + GitHub monitoring)
/home/claude/launcher.py "$@"

echo ""
echo "Claude Code exited. Dropping to bash shell..."
echo "Type 'exit' to close the container."
echo ""

exec bash
