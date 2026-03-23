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

FIREWALL_CONFIG_DIR="${PWD}/.firewall"

mkdir -p "$FIREWALL_CONFIG_DIR"

if [ -z "$DISABLE_FIREWALL" ]; then
    # Determine upstream: if HTTP_PROXY is set (mitmproxy on host), chain through it.
    # allowlist-proxy itself must NOT use the outer HTTP_PROXY env var — it IS the proxy.
    UPSTREAM_ARG=""
    if [ -n "$HTTP_PROXY" ]; then
        UPSTREAM_ARG="--upstream $HTTP_PROXY"
    fi

    echo "Starting allowlist proxy (listen :3128, upstream: ${HTTP_PROXY:-direct})..."
    # Run as proxyuser so iptables --uid-owner rules can allow only the proxy
    # process to make direct outbound TCP connections.
    sudo -u proxyuser \
        env -u HTTP_PROXY -u HTTPS_PROXY \
        /usr/local/bin/allowlist-proxy \
            --listen 127.0.0.1:3128 \
            $UPSTREAM_ARG \
        > "$FIREWALL_CONFIG_DIR/proxy.log" 2>&1 &
    PROXY_PID=$!
    echo "Allowlist proxy started (PID $PROXY_PID)"
    # Brief pause to confirm it bound successfully
    sleep 0.3
    if ! kill -0 $PROXY_PID 2>/dev/null; then
        echo "ERROR: allowlist-proxy failed to start. Check $FIREWALL_CONFIG_DIR/proxy.log"
        cat "$FIREWALL_CONFIG_DIR/proxy.log"
        exit 1
    fi

    # Route Claude's traffic through the allowlist proxy.
    # Explicitly clear NO_PROXY/no_proxy so nothing can bypass the allowlist
    # by setting no_proxy=some.domain (curl, Python requests, etc. all honour it).
    export HTTP_PROXY=http://127.0.0.1:3128
    export HTTPS_PROXY=http://127.0.0.1:3128
    export NO_PROXY=""
    export no_proxy=""

    # Enforce at the network level: only proxyuser may make direct outbound TCP
    # connections. All other processes must connect via the allowlist proxy on
    # 127.0.0.1:3128. This closes the no_proxy / direct-connect bypass regardless
    # of which tool or env var is used.
    #
    # Rules (evaluated top-to-bottom in OUTPUT chain):
    #   1. Allow loopback (proxy listens here; clients connect here)
    #   2. Allow DNS (UDP 53) so resolution still works for all processes
    #   3. Allow proxyuser to make direct outbound TCP (proxy's own connections)
    #   4. Reject all other outbound TCP
    echo "Applying iptables egress enforcement..."
    if sudo iptables -F OUTPUT 2>/dev/null && \
       sudo iptables -A OUTPUT -o lo -j ACCEPT && \
       sudo iptables -A OUTPUT -p udp --dport 53 -j ACCEPT && \
       sudo iptables -A OUTPUT -p tcp -m owner --uid-owner proxyuser -j ACCEPT && \
       sudo iptables -A OUTPUT -p tcp -j REJECT --reject-with tcp-reset; then
        echo "✅ iptables egress rules applied — only proxyuser may make direct TCP connections"
    else
        echo "⚠️  WARNING: iptables not available — no_proxy bypass is possible"
    fi

    echo ""
    echo "Proxy log: $FIREWALL_CONFIG_DIR/proxy.log"
    echo ""
else
    echo "WARNING: FIREWALL IS DISABLED - Claude has unrestricted network access!"
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
