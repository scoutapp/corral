#!/bin/bash
# Sandclaude entrypoint - Starts Python launcher with Linear monitoring
# After Claude exits, drop to an interactive bash shell

# Configure mitmproxy CA certificate if proxy is enabled
echo "🔒 Proxy configuration:"
echo "   HTTP_PROXY=${HTTP_PROXY:-not set}"
echo "   HTTPS_PROXY=${HTTPS_PROXY:-not set}"

if [ -n "$HTTP_PROXY" ]; then
    echo "   Certificate path: $HOME/.mitmproxy/mitmproxy-ca-cert.pem"

    if [ -f "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        echo "   Certificate exists: ✅"

        # Export environment variables to trust mitmproxy certificate
        export SSL_CERT_FILE="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"
        export REQUESTS_CA_BUNDLE="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"
        export NODE_EXTRA_CA_CERTS="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"
        export CURL_CA_BUNDLE="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"

        echo "   SSL_CERT_FILE=$SSL_CERT_FILE"
        echo "   REQUESTS_CA_BUNDLE=$REQUESTS_CA_BUNDLE"
        echo "   NODE_EXTRA_CA_CERTS=$NODE_EXTRA_CA_CERTS"
        echo "✅ Proxy mode enabled"
    else
        echo "   Certificate exists: ❌"
        echo "⚠️  Warning: Start 'bash start-proxy.sh' on host first"
    fi
else
    echo "   Status: Proxy disabled"
fi
echo ""

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
