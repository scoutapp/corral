#!/bin/bash
# Root entrypoint - Runs privileged setup, then drops to claude user
set -e

# Enable debug mode if requested
if [ -n "$SANDCLAUDE_DEBUG" ]; then
    set -x
    DEBUG=1
else
    DEBUG=0
fi

# Helper function for debug logging
debug_log() {
    if [ "$DEBUG" = "1" ]; then
        echo "DEBUG: $*"
    fi
}

# This script runs as root and performs all privileged operations:
# 1. Configure mitmproxy CA certificates (system-wide)
# 2. Start allowlist-proxy as proxyuser
# 3. Configure iptables
# 4. Start dockerd (if DinD enabled)
# 5. Drop to claude user and exec entrypoint-user.sh

CLAUDE_USER="claude"
CLAUDE_HOME="/home/claude"

# Configure mitmproxy CA certificate if proxy is enabled
debug_log "Proxy configuration:"
debug_log "   HTTP_PROXY=${HTTP_PROXY:-not set}"
debug_log "   HTTPS_PROXY=${HTTPS_PROXY:-not set}"

if [ -n "$HTTP_PROXY" ]; then
    debug_log "Certificate path: $CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem"

    if [ -f "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        debug_log "Certificate exists: ✅"

        # Install into system trust store so dockerd (DinD) trusts the proxy CA.
        MITM_CRT=/usr/local/share/ca-certificates/mitmproxy-ca.crt
        cp "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" "$MITM_CRT"
        MITM_HASH=$(openssl x509 -hash -noout -in "$MITM_CRT")
        ln -sf "$MITM_CRT" /etc/ssl/certs/mitmproxy-ca.pem
        ln -sf mitmproxy-ca.pem /etc/ssl/certs/${MITM_HASH}.0
        # Append to the bundle only if not already present (idempotent)
        if ! openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt 2>/dev/null \
                | openssl pkcs7 -print_certs -noout 2>/dev/null \
                | grep -q "mitmproxy"; then
            cat "$MITM_CRT" >> /etc/ssl/certs/ca-certificates.crt
        fi

        echo "✅ Proxy CA certificate installed"
    else
        echo "⚠️  Warning: Proxy certificate not found"
    fi
fi

mkdir -p "${CLAUDE_HOME}/logs"
chown -R $CLAUDE_USER:$CLAUDE_USER "${CLAUDE_HOME}/logs"
PROXY_LOG="${CLAUDE_HOME}/logs/proxy.log"
ALLOWLIST_ENC="${CLAUDE_HOME}/allowed-domains.txt.enc"

if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
    DISABLE_FIREWALL=""
fi

PROXY_PID=""

if [ -z "$DISABLE_FIREWALL" ]; then
    # Verify encrypted allowlist exists
    if [ ! -f "$ALLOWLIST_ENC" ]; then
        echo "ERROR: encrypted allowlist not found at $ALLOWLIST_ENC"
        echo "       Run 'sandclaude firewall-reload' on the host"
        exit 1
    fi

    # Note: ALLOWLIST_KEY is now embedded in the binary, not required as env var
    # We still accept it for backwards compatibility (encrypt subcommand)

    # Determine upstream: if HTTP_PROXY is set (mitmproxy on host), chain through it.
    UPSTREAM_ARG=""
    if [ -n "$HTTP_PROXY" ]; then
        UPSTREAM_ARG="--upstream $HTTP_PROXY"
    fi

    PASSTHROUGH_ARG=""
    if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
        ALLOWED_DOMAINS_TXT="${CLAUDE_HOME}/allowed-domains.txt"
        PASSTHROUGH_TMP="/tmp/passthrough-domains.txt"
        touch "$PASSTHROUGH_TMP"
        chown proxyuser:proxyuser "$PASSTHROUGH_TMP"
        PASSTHROUGH_ARG="--passthrough-log $PASSTHROUGH_TMP"
        echo "WARNING: PASSTHROUGH MODE — unknown domains will be allowed and written to:"
        echo "  $ALLOWED_DOMAINS_TXT (on host)"
        echo ""
    fi

    # Copy the encrypted allowlist to a location proxyuser can read
    ALLOWLIST_COPY="/tmp/allowed-domains.txt.enc"
    cp "$ALLOWLIST_ENC" "$ALLOWLIST_COPY"
    chmod 644 "$ALLOWLIST_COPY"

    # Create log file with write permissions for proxyuser
    touch "$PROXY_LOG"
    chmod 666 "$PROXY_LOG"

    debug_log "Starting allowlist proxy (listen :3128, upstream: ${HTTP_PROXY:-direct})..."
    # Run as proxyuser so iptables --uid-owner rules can allow only the proxy
    # Use gosu to avoid TTY issues
    gosu proxyuser env -u HTTP_PROXY -u HTTPS_PROXY \
        /usr/local/bin/allowlist-proxy \
            --listen 0.0.0.0:3128 \
            --allowlist $ALLOWLIST_COPY \
            $UPSTREAM_ARG \
            $PASSTHROUGH_ARG \
        >> $PROXY_LOG 2>&1 &
    PROXY_PID=$!
    echo "✅ Allowlist proxy started"

    DIND_PROXY_PORT=3128

    # In passthrough mode, sync new domains from the proxyuser-owned tmp file
    if [ -n "$DISABLE_FIREWALL_AND_WRITE" ]; then
        PASSTHROUGH_LOG="$CLAUDE_HOME/logs/passthrough-sync.log"
        (
            while true; do
                sleep 2
                if [ -f "$PASSTHROUGH_TMP" ]; then
                    while IFS= read -r domain; do
                        [ -z "$domain" ] && continue
                        if ! grep -qxF "$domain" "$ALLOWED_DOMAINS_TXT" 2>/dev/null; then
                            echo "$domain" >> "$ALLOWED_DOMAINS_TXT"
                            if [ "$DEBUG" = "1" ]; then
                                echo "[$(date '+%Y-%m-%d %H:%M:%S')] Synced domain: $domain"
                            fi
                        fi
                    done < "$PASSTHROUGH_TMP"
                fi
            done
        ) >> "$PASSTHROUGH_LOG" 2>&1 &
        debug_log "Started passthrough sync loop (logging to $PASSTHROUGH_LOG)"
    fi

    # Brief pause to confirm it bound successfully
    sleep 0.3
    if ! kill -0 $PROXY_PID 2>/dev/null; then
        echo "ERROR: allowlist-proxy failed to start. Check $PROXY_LOG"
        cat "$PROXY_LOG"
        exit 1
    fi

    # Enforce at the network level: only proxyuser may make direct outbound TCP
    debug_log "Applying iptables egress enforcement..."
    if iptables -F OUTPUT 2>/dev/null && \
       iptables -A OUTPUT -o lo -j ACCEPT && \
       iptables -A OUTPUT -p udp --dport 53 -j ACCEPT && \
       iptables -A OUTPUT -p tcp -m owner --uid-owner proxyuser -j ACCEPT && \
       iptables -A OUTPUT -p tcp -d 172.16.0.0/12 -j ACCEPT && \
       iptables -A OUTPUT -p tcp -j REJECT --reject-with tcp-reset; then
        echo "✅ Network firewall active"
    else
        echo "⚠️  WARNING: iptables not available"
    fi

    debug_log "Proxy log: $PROXY_LOG"
else
    echo "WARNING: FIREWALL IS DISABLED - Claude has unrestricted network access!"
    echo ""
fi

# ── DinD: start inner dockerd ─────────────────────────────────────────────
DOCKERD_PID=""
if [ -n "$DIND_ENABLED" ]; then
    echo "Starting inner dockerd..."

    DIND_SOCKET=/var/run/dind/docker.sock
    DIND_DATA=/var/lib/docker-dind
    STORAGE_DRIVER="${DIND_STORAGE_DRIVER:-vfs}"

    # Write daemon config
    mkdir -p /etc/docker-dind
    cat > /etc/docker-dind/daemon.json <<DAEMONCFG
{
  "data-root": "${DIND_DATA}",
  "hosts": ["unix://${DIND_SOCKET}"],
  "storage-driver": "${STORAGE_DRIVER}",
  "iptables": true,
  "ip-masq": false,
  "bip": "172.18.0.1/16",
  "proxies": {
    "http-proxy": "http://172.18.0.1:${DIND_PROXY_PORT}",
    "https-proxy": "http://172.18.0.1:${DIND_PROXY_PORT}",
    "no-proxy": "172.18.0.0/16,127.0.0.0/8"
  }
}
DAEMONCFG

    # Refresh system CA trust store before starting dockerd
    if [ -f "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        MITM_CRT=/usr/local/share/ca-certificates/mitmproxy-ca.crt
        cp "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" "$MITM_CRT"
        MITM_HASH=$(openssl x509 -hash -noout -in "$MITM_CRT")
        ln -sf "$MITM_CRT" /etc/ssl/certs/mitmproxy-ca.pem
        ln -sf mitmproxy-ca.pem /etc/ssl/certs/${MITM_HASH}.0
        if ! openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt 2>/dev/null \
                | openssl pkcs7 -print_certs -noout 2>/dev/null \
                | grep -q "mitmproxy"; then
            cat "$MITM_CRT" >> /etc/ssl/certs/ca-certificates.crt
        fi
    fi

    dockerd --config-file /etc/docker-dind/daemon.json \
        > "$CLAUDE_HOME/logs/dockerd.log" 2>&1 &
    DOCKERD_PID=$!

    # Wait for dockerd to be ready (poll socket, max 15s)
    debug_log "Waiting for inner dockerd..."
    for i in $(seq 1 30); do
        if docker --host="unix://${DIND_SOCKET}" info > /dev/null 2>&1; then
            echo "✅ Docker-in-Docker ready"
            break
        fi
        sleep 0.5
        if [ "$i" -eq 30 ]; then
            echo "ERROR: inner dockerd failed to start. Check $CLAUDE_HOME/logs/dockerd.log"
            cat "$CLAUDE_HOME/logs/dockerd.log"
            exit 1
        fi
    done

    # Export DOCKER_HOST so all docker commands use the inner daemon
    export DOCKER_HOST="unix://${DIND_SOCKET}"
    debug_log "DOCKER_HOST=${DOCKER_HOST}"

    # Make the proxy CA cert available for inner containers
    if [ -f "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        cp "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" /etc/proxy-ca.crt
        chmod 644 /etc/proxy-ca.crt
        debug_log "Proxy CA cert available at /etc/proxy-ca.crt"
    fi

    # Enable IP forwarding (required for docker bridge networking)
    sysctl -w net.ipv4.ip_forward=1 > /dev/null

    # PREROUTING REDIRECT: intercept inner container TCP egress -> allowlist proxy
    iptables -t nat -A PREROUTING \
        -s 172.16.0.0/12 \
        -p tcp \
        ! -d 172.16.0.0/12 \
        -j REDIRECT --to-port 3128

    # FORWARD rules: allow bridge <-> external interface traffic
    iptables -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
    iptables -A FORWARD -s 172.16.0.0/12 -j ACCEPT
    iptables -A FORWARD -d 172.16.0.0/12 -j ACCEPT

    debug_log "Inner container proxy enforcement applied (172.16.0.0/12 -> :3128)"

    # Start cert injector as claude user
    if [ -f /etc/proxy-ca.crt ]; then
        gosu $CLAUDE_USER /home/claude/bin/cert-injector >> ${CLAUDE_HOME}/logs/cert-injector.log 2>&1 &
        CERT_INJ_PID=$!
        debug_log "Cert injector started (PID $CERT_INJ_PID)"
    fi
fi

# Check if GitHub integration is configured
if [ -n "$GITHUB_REPO" ]; then
    debug_log "GitHub integration: $GITHUB_REPO"
fi

# Cleanup function for root-level processes
cleanup_root() {
    if [ -n "$DOCKERD_PID" ] && [ "$DOCKERD_PID" != "" ]; then
        echo "Stopping inner containers..."
        docker --host="unix:///var/run/dind/docker.sock" \
            ps -q 2>/dev/null | xargs -r docker --host="unix:///var/run/dind/docker.sock" stop --time=5 2>/dev/null || true
        echo "Stopping inner dockerd (PID $DOCKERD_PID)..."
        kill "$DOCKERD_PID" 2>/dev/null || true
        wait "$DOCKERD_PID" 2>/dev/null || true
    fi
    if [ -n "$PROXY_PID" ] && [ "$PROXY_PID" != "" ]; then
        echo "Stopping proxy (PID $PROXY_PID)..."
        kill "$PROXY_PID" 2>/dev/null || true
    fi
}
trap cleanup_root EXIT

# Export environment for claude user
# Route Claude's traffic through the allowlist proxy
if [ -z "$DISABLE_FIREWALL" ]; then
    export HTTP_PROXY=http://127.0.0.1:3128
    export HTTPS_PROXY=http://127.0.0.1:3128
    export NO_PROXY=""
    export no_proxy=""
fi

# Export cert env vars for claude user
if [ -f "$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
    export SSL_CERT_FILE="$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem"
    export REQUESTS_CA_BUNDLE="$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem"
    export NODE_EXTRA_CA_CERTS="$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem"
    export CURL_CA_BUNDLE="$CLAUDE_HOME/.mitmproxy/mitmproxy-ca-cert.pem"
fi

# Configure ~/.docker/config.json for DinD proxy injection
if [ -n "$DIND_ENABLED" ]; then
    # Create docker config as root, then chown to claude
    mkdir -p /home/claude/.docker
    cat > /home/claude/.docker/config.json <<DOCKERCFG
{
  "proxies": {
    "default": {
      "httpProxy": "http://172.18.0.1:${DIND_PROXY_PORT}",
      "httpsProxy": "http://172.18.0.1:${DIND_PROXY_PORT}",
      "noProxy": "172.18.0.0/16,127.0.0.0/8"
    }
  }
}
DOCKERCFG
    chown -R $CLAUDE_USER:$CLAUDE_USER /home/claude/.docker
fi

# Drop to claude user and execute the user entrypoint
debug_log "Dropping to claude user..."

# Use gosu to drop privileges - it properly handles TTY and is designed for containers
# Export all required environment variables
cd "$CLAUDE_HOME"

exec gosu $CLAUDE_USER \
    env \
        HOME="$CLAUDE_HOME" \
        USER="$CLAUDE_USER" \
        DOCKER_HOST="$DOCKER_HOST" \
        HTTP_PROXY="$HTTP_PROXY" \
        HTTPS_PROXY="$HTTPS_PROXY" \
        NO_PROXY="$NO_PROXY" \
        no_proxy="$no_proxy" \
        SSL_CERT_FILE="$SSL_CERT_FILE" \
        REQUESTS_CA_BUNDLE="$REQUESTS_CA_BUNDLE" \
        NODE_EXTRA_CA_CERTS="$NODE_EXTRA_CA_CERTS" \
        CURL_CA_BUNDLE="$CURL_CA_BUNDLE" \
        GITHUB_REPO="$GITHUB_REPO" \
        TERM="$TERM" \
        PATH="$PATH" \
    /bin/bash /home/claude/entrypoint-user.sh "$@"
