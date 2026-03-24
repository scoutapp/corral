#!/bin/bash
# Sandclaude entrypoint - Starts Python launcher with Linear monitoring
# After Claude exits, drop to an interactive bash shell

# Cleanup inner dockerd and containers on exit
DOCKERD_PID=""
cleanup_dind() {
    if [ -n "$DOCKERD_PID" ]; then
        echo "Stopping inner containers..."
        docker --host="unix:///var/run/dind/docker.sock" \
            ps -q 2>/dev/null | xargs -r docker --host="unix:///var/run/dind/docker.sock" stop --time=5 2>/dev/null || true
        echo "Stopping inner dockerd (PID $DOCKERD_PID)..."
        sudo kill "$DOCKERD_PID" 2>/dev/null || true
        wait "$DOCKERD_PID" 2>/dev/null || true
    fi
}
trap cleanup_dind EXIT

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

mkdir -p "${HOME}/logs"
PROXY_LOG="${HOME}/logs/proxy.log"
ALLOWLIST_ENC="${HOME}/allowed-domains.txt.enc"

if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
    DISABLE_FIREWALL=""
fi

if [ -z "$DISABLE_FIREWALL" ]; then
    # Verify encrypted allowlist exists
    if [ ! -f "$ALLOWLIST_ENC" ]; then
        echo "ERROR: encrypted allowlist not found at $ALLOWLIST_ENC"
        echo "       Run 'sandclaude firewall-reload' on the host"
        exit 1
    fi

    # Verify encryption key is provided
    if [ -z "$ALLOWLIST_KEY" ]; then
        echo "ERROR: ALLOWLIST_KEY environment variable is not set"
        echo "       Run 'sandclaude init' to generate the key"
        exit 1
    fi

    # Determine upstream: if HTTP_PROXY is set (mitmproxy on host), chain through it.
    # allowlist-proxy itself must NOT use the outer HTTP_PROXY env var — it IS the proxy.
    UPSTREAM_ARG=""
    if [ -n "$HTTP_PROXY" ]; then
        UPSTREAM_ARG="--upstream $HTTP_PROXY"
    fi

    PASSTHROUGH_ARG=""
    if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
        ALLOWED_DOMAINS_TXT="${HOME}/allowed-domains.txt"
        # Write to a /tmp path owned by proxyuser to avoid bind-mount permission issues
        # (on macOS Docker Desktop, VirtioFS doesn't reliably honor chmod for non-owners).
        PASSTHROUGH_TMP="/tmp/passthrough-domains.txt"
        sudo -u proxyuser touch "$PASSTHROUGH_TMP"
        PASSTHROUGH_ARG="--passthrough-log $PASSTHROUGH_TMP"
        echo "WARNING: PASSTHROUGH MODE — unknown domains will be allowed and written to:"
        echo "  $ALLOWED_DOMAINS_TXT (on host)"
        echo ""
    fi

    # Copy the encrypted allowlist to a location proxyuser can read
    # (the mounted file is owned by the host user, proxyuser can't read it directly)
    ALLOWLIST_COPY="/tmp/allowed-domains.txt.enc"
    cp "$ALLOWLIST_ENC" "$ALLOWLIST_COPY"
    chmod 644 "$ALLOWLIST_COPY"

    # Create log file with write permissions for proxyuser
    touch "$PROXY_LOG"
    chmod 666 "$PROXY_LOG"

    echo "Starting allowlist proxy (listen :3128, upstream: ${HTTP_PROXY:-direct})..."
    # Run as proxyuser so iptables --uid-owner rules can allow only the proxy
    # process to make direct outbound TCP connections.
    sudo -u proxyuser \
        env -u HTTP_PROXY -u HTTPS_PROXY ALLOWLIST_KEY="$ALLOWLIST_KEY" \
        /usr/local/bin/allowlist-proxy \
            --listen 127.0.0.1:3128 \
            --allowlist "$ALLOWLIST_COPY" \
            $UPSTREAM_ARG \
            $PASSTHROUGH_ARG \
        >> "$PROXY_LOG" 2>&1 &
    PROXY_PID=$!
    echo "Allowlist proxy started (PID $PROXY_PID)"

    # In passthrough mode, sync new domains from the proxyuser-owned tmp file
    # to the bind-mounted file (which claude user can write to).
    if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
        (
            while true; do
                sleep 2
                if [ -f "$PASSTHROUGH_TMP" ]; then
                    while IFS= read -r domain; do
                        [ -z "$domain" ] && continue
                        if ! grep -qxF "$domain" "$ALLOWED_DOMAINS_TXT" 2>/dev/null; then
                            echo "$domain" >> "$ALLOWED_DOMAINS_TXT"
                        fi
                    done < "$PASSTHROUGH_TMP"
                fi
            done
        ) &
    fi
    # Brief pause to confirm it bound successfully
    sleep 0.3
    if ! kill -0 $PROXY_PID 2>/dev/null; then
        echo "ERROR: allowlist-proxy failed to start. Check $PROXY_LOG"
        cat "$PROXY_LOG"
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
    echo "Proxy log: $PROXY_LOG"
    echo ""
else
    echo "WARNING: FIREWALL IS DISABLED - Claude has unrestricted network access!"
    echo ""
fi

# ── DinD: start inner dockerd ─────────────────────────────────────────────
if [ -n "$DIND_ENABLED" ]; then
    echo "Starting inner dockerd..."

    DIND_SOCKET=/var/run/dind/docker.sock
    DIND_DATA=/var/lib/docker-dind
    STORAGE_DRIVER="${DIND_STORAGE_DRIVER:-overlay2}"

    # Write daemon config
    sudo mkdir -p /etc/docker-dind
    sudo tee /etc/docker-dind/daemon.json > /dev/null <<DAEMONCFG
{
  "data-root": "${DIND_DATA}",
  "hosts": ["unix://${DIND_SOCKET}"],
  "storage-driver": "${STORAGE_DRIVER}",
  "iptables": true,
  "ip-masq": false,
  "bip": "172.18.0.1/16"
}
DAEMONCFG

    sudo dockerd --config-file /etc/docker-dind/daemon.json \
        > "$FIREWALL_CONFIG_DIR/dockerd.log" 2>&1 &
    DOCKERD_PID=$!

    # Wait for dockerd to be ready (poll socket, max 15s)
    echo "Waiting for inner dockerd..."
    for i in $(seq 1 30); do
        if sudo docker --host="unix://${DIND_SOCKET}" info > /dev/null 2>&1; then
            echo "✅ Inner dockerd ready (PID $DOCKERD_PID)"
            break
        fi
        sleep 0.5
        if [ "$i" -eq 30 ]; then
            echo "ERROR: inner dockerd failed to start. Check $FIREWALL_CONFIG_DIR/dockerd.log"
            cat "$FIREWALL_CONFIG_DIR/dockerd.log"
            exit 1
        fi
    done

    # Export DOCKER_HOST so all docker commands use the inner daemon
    export DOCKER_HOST="unix://${DIND_SOCKET}"

    # Configure ~/.docker/config.json to inject proxy into inner containers
    mkdir -p /home/claude/.docker
    cat > /home/claude/.docker/config.json <<'DOCKERCFG'
{
  "proxies": {
    "default": {
      "httpProxy": "http://127.0.0.1:3128",
      "httpsProxy": "http://127.0.0.1:3128",
      "noProxy": "172.18.0.0/16,127.0.0.0/8"
    }
  }
}
DOCKERCFG

    # Enable IP forwarding (required for docker bridge networking)
    sudo sysctl -w net.ipv4.ip_forward=1 > /dev/null

    # PREROUTING REDIRECT: intercept inner container TCP egress -> allowlist proxy
    # Inner containers (172.18.0.0/16) trying to reach external hosts get redirected
    # to the allowlist proxy on 127.0.0.1:3128 before being forwarded.
    sudo iptables -t nat -A PREROUTING \
        -s 172.18.0.0/16 \
        ! -d 172.18.0.0/16 \
        ! -d 127.0.0.0/8 \
        -p tcp \
        -j REDIRECT --to-port 3128

    # FORWARD rules: allow bridge <-> external interface traffic
    sudo iptables -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
    sudo iptables -A FORWARD -s 172.18.0.0/16 -j ACCEPT
    sudo iptables -A FORWARD -d 172.18.0.0/16 -j ACCEPT

    echo "✅ Inner container proxy enforcement applied (172.18.0.0/16 -> :3128)"
    echo "   DOCKER_HOST=${DOCKER_HOST}"
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
