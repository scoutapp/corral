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

        # Install into system trust store so dockerd (DinD) trusts the proxy CA.
        # We bypass update-ca-certificates because it uses change-detection that skips
        # regenerating the bundle if symlinks already exist from a prior image layer.
        # Direct install is reliable: copy cert, create symlinks, append to bundle.
        MITM_CRT=/usr/local/share/ca-certificates/mitmproxy-ca.crt
        sudo cp "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" "$MITM_CRT"
        MITM_HASH=$(openssl x509 -hash -noout -in "$MITM_CRT")
        sudo ln -sf "$MITM_CRT" /etc/ssl/certs/mitmproxy-ca.pem
        sudo ln -sf mitmproxy-ca.pem /etc/ssl/certs/${MITM_HASH}.0
        # Append to the bundle only if not already present (idempotent)
        if ! openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt 2>/dev/null \
                | openssl pkcs7 -print_certs -noout 2>/dev/null \
                | grep -q "mitmproxy"; then
            sudo sh -c "cat '$MITM_CRT' >> /etc/ssl/certs/ca-certificates.crt"
        fi

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

    # Selective mitm: always point --monitorlist at /tmp/monitor-hosts.txt, even if
    # the file doesn't exist yet — the proxy treats an absent monitor file as
    # "monitor all" (the default). Seeding the flag unconditionally is what lets a
    # project ENABLE the monitor-list on a running container without a restart:
    # `sandclaude proxy-apply`/the dashboard docker-cp the file in and SIGHUP, and
    # the proxy (already watching that path) picks it up live.
    MONITOR_COPY="/tmp/monitor-hosts.txt"
    MONITOR_SRC="${HOME}/monitor-hosts.txt"
    if [ -f "$MONITOR_SRC" ]; then
        cp "$MONITOR_SRC" "$MONITOR_COPY"
        chmod 644 "$MONITOR_COPY"
    fi
    MONITOR_ARG="--monitorlist $MONITOR_COPY"

    # mitm-ports: forwarded from the host config via env (default handled by the proxy).
    MITM_PORTS_ARG=""
    if [ -n "$SANDCLAUDE_MITM_PORTS" ]; then
        MITM_PORTS_ARG="--mitm-ports $SANDCLAUDE_MITM_PORTS"
    fi

    # Create log file with write permissions for proxyuser
    touch "$PROXY_LOG"
    chmod 666 "$PROXY_LOG"

    # Transparent (intercepting) listener port. iptables REDIRECT sends captured
    # connections here; the proxy recovers the original destination via
    # SO_ORIGINAL_DST and the SNI/Host, so clients need no proxy env vars.
    TRANSPARENT_PORT=3129

    echo "Starting allowlist proxy (explicit :3128, transparent :$TRANSPARENT_PORT, upstream: ${HTTP_PROXY:-direct})..."
    # Run as proxyuser so iptables --uid-owner rules can allow only the proxy
    # process to make direct outbound TCP connections.
    sudo -u proxyuser \
        env -u HTTP_PROXY -u HTTPS_PROXY ALLOWLIST_KEY="$ALLOWLIST_KEY" \
        /usr/local/bin/allowlist-proxy \
            --listen 0.0.0.0:3128 \
            --transparent-listen 0.0.0.0:$TRANSPARENT_PORT \
            --allowlist "$ALLOWLIST_COPY" \
            $MONITOR_ARG \
            $MITM_PORTS_ARG \
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

    # Route the outer container's own processes (Claude, curl, etc.) through the
    # allowlist proxy via explicit env vars. Transparent capture of *outer*
    # (locally-generated) traffic via nat OUTPUT REDIRECT / TPROXY does not work
    # reliably inside Docker Desktop's LinuxKit VM (REDIRECT-to-loopback is not
    # delivered; the OUTPUT→lo TPROXY reroute does not complete), so we keep the
    # explicit env here. This is ONE place set once — not per-app config. Inner
    # DinD containers ARE captured transparently (PREROUTING, see below) and need
    # no env vars. Export both cases; clear NO_PROXY so nothing bypasses.
    export HTTP_PROXY=http://127.0.0.1:3128
    export HTTPS_PROXY=http://127.0.0.1:3128
    export http_proxy=http://127.0.0.1:3128
    export https_proxy=http://127.0.0.1:3128
    export NO_PROXY=""
    export no_proxy=""

    # Enforce at the network level: only proxyuser may make direct outbound TCP
    # connections. All other processes must connect via the allowlist proxy on
    # 127.0.0.1:3128. This closes the no_proxy / direct-connect bypass regardless
    # of which tool or env var is used.
    #
    # Rules (evaluated top-to-bottom in OUTPUT chain):
    #   1. Allow loopback (proxy listens here; clients connect here)
    #   2. Allow ESTABLISHED/RELATED — response packets for inbound connections
    #      (e.g. Docker Desktop port-forwarding to a bound service). Without this
    #      the app's reply hits the REJECT rule because the destination IP is
    #      outside 172.16.0.0/12.
    #   3. Allow DNS (UDP 53) so resolution still works for all processes
    #   4. Allow proxyuser to make direct outbound TCP (proxy's own connections)
    #   5. Allow TCP to DinD bridge networks so the proxy can accept connections
    #      from inner containers and respond to them (SYN-ACK, data) without
    #      the REJECT rule blocking the response packets.
    #   6. Reject all other outbound TCP
    echo "Applying iptables egress enforcement..."
    if sudo iptables -F OUTPUT 2>/dev/null && \
       sudo iptables -A OUTPUT -o lo -j ACCEPT && \
       sudo iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT && \
       sudo iptables -A OUTPUT -p udp --dport 53 -j ACCEPT && \
       sudo iptables -A OUTPUT -p tcp -m owner --uid-owner proxyuser -j ACCEPT && \
       sudo iptables -A OUTPUT -p tcp -d 172.16.0.0/12 -j ACCEPT && \
       sudo iptables -A OUTPUT -p tcp -j REJECT --reject-with tcp-reset; then
        echo "✅ iptables egress rules applied — only proxyuser may make direct TCP connections"
    else
        echo "⚠️  WARNING: iptables not available — no_proxy bypass is possible"
    fi

    # NOTE: there is intentionally NO nat OUTPUT REDIRECT for the outer container's
    # own traffic. Transparent capture of locally-generated packets does not work
    # reliably in Docker Desktop's LinuxKit VM (REDIRECT to 127.0.0.1 from OUTPUT is
    # not delivered to the listener even with route_localnet; the OUTPUT→lo TPROXY
    # reroute marks/routes packets but never completes). Outer processes therefore
    # use the explicit HTTP_PROXY env vars set above, enforced by the filter OUTPUT
    # REJECT. Inner DinD containers are captured transparently via PREROUTING below.

    echo ""
    echo "Proxy log: $PROXY_LOG"
    echo ""
else
    echo "WARNING: FIREWALL IS DISABLED - Claude has unrestricted network access!"
    echo ""
fi

# Ensure bin/ scripts are executable (needed when bin/ is volume-mounted from host)
chmod +x /home/claude/bin/* 2>/dev/null || true

# ── DinD: start inner dockerd ─────────────────────────────────────────────
if [ -n "$DIND_ENABLED" ]; then
    echo "Starting inner dockerd..."

    DIND_SOCKET=/var/run/dind/docker.sock
    DIND_DATA=/var/lib/docker-dind
    STORAGE_DRIVER="${DIND_STORAGE_DRIVER:-vfs}"

    # Write daemon config
    sudo mkdir -p /etc/docker-dind
    # NO "proxies" block here on purpose: a daemon.json "proxies" block would inject
    # HTTP_PROXY env into every inner CONTAINER (defeating transparent capture). The
    # daemon's OWN pulls are instead proxied via dockerd's process environment
    # (HTTP_PROXY set when we launch dockerd below) — that affects only the daemon,
    # not the containers it starts.
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

    # Refresh system CA trust store right before starting dockerd so it picks
    # up the mitmproxy cert even if it wasn't fully available at container start.
    # Re-run the same direct install (idempotent) in case the cert wasn't readable
    # at container start time (VirtioFS bind-mount timing on macOS).
    if [ -f "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        MITM_CRT=/usr/local/share/ca-certificates/mitmproxy-ca.crt
        sudo cp "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" "$MITM_CRT"
        MITM_HASH=$(openssl x509 -hash -noout -in "$MITM_CRT")
        sudo ln -sf "$MITM_CRT" /etc/ssl/certs/mitmproxy-ca.pem
        sudo ln -sf mitmproxy-ca.pem /etc/ssl/certs/${MITM_HASH}.0
        if ! openssl crl2pkcs7 -nocrl -certfile /etc/ssl/certs/ca-certificates.crt 2>/dev/null \
                | openssl pkcs7 -print_certs -noout 2>/dev/null \
                | grep -q "mitmproxy"; then
            sudo sh -c "cat '$MITM_CRT' >> /etc/ssl/certs/ca-certificates.crt"
        fi
    fi

    # Launch dockerd with HTTP(S)_PROXY in ITS environment so the daemon's own
    # registry pulls go through the allowlist proxy (the daemon runs in the outer
    # container, so it reaches the proxy at 127.0.0.1:3128). This proxies only the
    # daemon, not the containers it starts (those are captured transparently via
    # PREROUTING). NO_PROXY covers the inner bridge + loopback so intra-DinD and
    # local API traffic is not proxied.
    sudo env \
        HTTP_PROXY=http://127.0.0.1:3128 \
        HTTPS_PROXY=http://127.0.0.1:3128 \
        NO_PROXY=172.16.0.0/12,127.0.0.0/8,localhost \
        dockerd --config-file /etc/docker-dind/daemon.json \
        > "$HOME/logs/dockerd.log" 2>&1 &
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
            echo "ERROR: inner dockerd failed to start. Check $HOME/logs/dockerd.log"
            cat "$HOME/logs/dockerd.log"
            exit 1
        fi
    done

    # Export DOCKER_HOST so all docker commands use the inner daemon
    export DOCKER_HOST="unix://${DIND_SOCKET}"

    # ~/.docker/config.json no longer injects proxy env vars into inner containers
    # — transparent PREROUTING capture handles their egress. Keep an empty config
    # so docker CLI has a valid file to read.
    mkdir -p /home/claude/.docker
    cat > /home/claude/.docker/config.json <<DOCKERCFG
{}
DOCKERCFG

    # Make the proxy CA cert available at a well-known path.
    # The cert-injector daemon will automatically inject it into every new inner container.
    if [ -f "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]; then
        sudo cp "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" /etc/proxy-ca.crt
        sudo chmod 644 /etc/proxy-ca.crt
        echo "✅ Proxy CA cert available at /etc/proxy-ca.crt"
    fi

    # Enable IP forwarding (required for docker bridge networking)
    sudo sysctl -w net.ipv4.ip_forward=1 > /dev/null

    # PREROUTING REDIRECT: intercept inner container TCP egress -> allowlist proxy.
    # Inner containers (172.16.0.0/12) reaching external hosts are redirected to the
    # transparent listener, which recovers the original destination via
    # SO_ORIGINAL_DST and the SNI/Host — so inner containers need NO proxy env vars.
    DIND_TRANSPARENT_PORT="${TRANSPARENT_PORT:-3129}"
    sudo iptables -t nat -A PREROUTING \
        -s 172.16.0.0/12 \
        -p tcp \
        ! -d 172.16.0.0/12 \
        -j REDIRECT --to-port "$DIND_TRANSPARENT_PORT"

    # FORWARD rules: allow bridge <-> external interface traffic
    sudo iptables -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
    sudo iptables -A FORWARD -s 172.16.0.0/12 -j ACCEPT
    sudo iptables -A FORWARD -d 172.16.0.0/12 -j ACCEPT

    # MASQUERADE inner-container egress that is NOT redirected to the proxy —
    # specifically DNS (UDP 53) to the external resolver. Inner containers now do
    # their own DNS resolution (no proxy env injected), so their UDP queries must be
    # SNAT'd to the outer container's IP to reach the resolver and have replies
    # routed back. (TCP to external hosts is handled by the PREROUTING REDIRECT
    # above and never leaves via this path.) daemon.json sets ip-masq=false, so we
    # add it here.
    #
    # IMPORTANT: scope this to the SAME range as the REDIRECT (172.16.0.0/12), not
    # just the default bridge (172.18.0.0/16). docker-compose apps create their own
    # networks (172.19.x, 172.20.x, …); a container on one of those still gets its
    # TCP redirected, but without masquerade its DNS replies can't route back, so it
    # fails with "can't reach the network" — which looks like the proxy is broken.
    sudo iptables -t nat -A POSTROUTING -s 172.16.0.0/12 ! -d 172.16.0.0/12 -j MASQUERADE

    echo "✅ Inner container proxy enforcement applied (172.16.0.0/12 -> :$DIND_TRANSPARENT_PORT)"
    echo "   DOCKER_HOST=${DOCKER_HOST}"
    echo ""

    # Start cert injector: watches for new inner containers and injects the
    # mitmproxy CA cert so they trust the allowlist proxy's TLS interception.
    if [ -f /etc/proxy-ca.crt ]; then
        /home/claude/bin/cert-injector >> "${HOME}/logs/cert-injector.log" 2>&1 &
        echo "✅ Cert injector started (PID $!)"
        echo ""
    fi
fi

# Launch Python launcher
/home/claude/launcher.py "$@"

echo ""
echo "Claude Code exited. Dropping to bash shell..."
echo "Type 'exit' to close the container."
echo ""

exec bash
