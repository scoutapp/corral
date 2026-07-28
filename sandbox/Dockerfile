# ── Stage 1: build allowlist-proxy ───────────────────────────────────────────
FROM golang:1.22-alpine AS proxy-builder

WORKDIR /build
COPY allowlist-proxy/ .
# CGO_ENABLED=0 produces a fully static binary — no libc needed in final image.
# Build the whole package (main.go + transparent.go), not a single file.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o allowlist-proxy .

# ── Stage 2: runtime image ────────────────────────────────────────────────────
FROM --platform=linux/amd64 ubuntu:24.04

# Copy the static proxy binary from the builder stage
COPY --from=proxy-builder /build/allowlist-proxy /usr/local/bin/allowlist-proxy

# Copy Go toolchain so sandclaude can be rebuilt inside the container
COPY --from=proxy-builder /usr/local/go /usr/local/go

ENV DEBIAN_FRONTEND=noninteractive

# Install base tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    curl \
    wget \
    git \
    ca-certificates \
    gnupg \
    jq \
    unzip \
    openssh-client \
    make \
    build-essential \
    sudo \
    vim \
    less \
    iptables \
    iproute2 \
    libcap2-bin \
    tcpdump \
    tmux \
    && rm -rf /var/lib/apt/lists/*

# Install proxy CA cert so subsequent curl/apt/npm HTTPS steps trust the MITM proxy.
# PROXY_CA_CERT is base64-encoded PEM, injected at build time by sandclaude.
ARG PROXY_CA_CERT=""
RUN if [ -n "$PROXY_CA_CERT" ]; then \
        printf "%s" "$PROXY_CA_CERT" | base64 -d \
            > /usr/local/share/ca-certificates/proxy-ca.crt \
        && update-ca-certificates; \
    fi
# npm uses its own CA bundle; point it at the system bundle which includes the proxy CA.
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt

# Install Node.js 22
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Playwright CLI and Chromium with system dependencies
# Use a world-readable path so the claude user can access browsers without sudo
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
ENV NODE_PATH=/usr/lib/node_modules
# PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 skips the ~200MB Chromium download (useful when
# the build proxy cannot stream large responses). Set to empty string to download.
ARG PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=
RUN npm install -g playwright && \
    if [ -z "$PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD" ]; then \
        playwright install --with-deps chromium && \
        chmod -R o+rx /ms-playwright; \
    fi

# Install Python 3, mitmproxy, and selenium
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
mitmproxy \
    && rm -rf /var/lib/apt/lists/*
  
# Install gh (GitHub CLI)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y --no-install-recommends gh && \
    rm -rf /var/lib/apt/lists/*

# Install Docker Engine (dockerd + CLI) and tini for DinD support
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
       -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
       https://download.docker.com/linux/ubuntu noble stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       docker-ce \
       docker-ce-cli \
       containerd.io \
       docker-compose-plugin \
       docker-buildx-plugin \
       tini \
    && rm -rf /var/lib/apt/lists/*

# Create directories for inner dockerd state and socket
RUN mkdir -p /var/lib/docker-dind /var/run/dind \
    && groupadd -f docker \
    && chmod 775 /var/run/dind

# Create a dedicated user for the allowlist proxy (fixed UID 900).
# iptables --uid-owner rules use this UID to allow only the proxy process
# to make direct outbound TCP connections; all other traffic must go through it.
RUN useradd -r -u 900 -s /usr/sbin/nologin proxyuser

# Create non-root user matching host UID/GID
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN groupadd -f -g ${GROUP_ID} claude && \
    useradd -m -u ${USER_ID} -g claude -o claude && \
    usermod -aG docker claude && \
    echo "claude ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/claude && \
    echo "claude ALL=(proxyuser) NOPASSWD: /usr/local/bin/allowlist-proxy" >> /etc/sudoers.d/claude

RUN chmod 0440 /etc/sudoers.d/claude

USER claude
ENV HOME=/home/claude
ENV TERM=xterm-256color

# Install uv
RUN curl -LsSf https://astral.sh/uv/install.sh | sh
ENV PATH="/home/claude/.cargo/bin:${PATH}"

# Install Claude Code
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH="/home/claude/.claude/bin:/home/claude/.local/bin:/usr/local/go/bin:${PATH}"

# Pre-create .mitmproxy owned by claude so mitmweb can write its CA keypair.
# Without this, Docker creates the directory as root:root when bind-mounting
# the host's mitmproxy-ca-cert.pem file, and mitmweb fails on startup.
RUN mkdir -p /home/claude/.mitmproxy && chown claude:claude /home/claude/.mitmproxy

# Copy launcher, entrypoint, skills, and bin scripts. The bin/ scripts live in
# two source dirs by tier — setup/ (sandbox self-config) and dind/ (the
# sandbox->inner-container bridge) — but flatten into a single /home/claude/bin/
# because entrypoint.sh, launcher.py, and the skills reference that flat path.
# NOTE: proxy-addon.py is NOT copied here — it is a HOST-tier asset loaded by the
# host's mitmweb (see host/proxy-addon.py), never used inside the container.
COPY --chown=claude:claude launcher.py /home/claude/launcher.py
COPY --chown=claude:claude entrypoint.sh /home/claude/entrypoint.sh
COPY --chown=claude:claude skills/ /home/claude/.claude/skills/
COPY --chown=claude:claude setup/ /home/claude/bin/
COPY --chown=claude:claude dind/ /home/claude/bin/
RUN chmod +x /home/claude/launcher.py /home/claude/entrypoint.sh \
    /home/claude/bin/cert-injector /home/claude/bin/docker

# Put ~/bin first in PATH so the `docker` wrapper (CA injection for builds)
# shadows /usr/bin/docker. The wrapper calls the real binary at /usr/bin/docker.
ENV PATH="/home/claude/bin:${PATH}"

ENTRYPOINT ["/usr/bin/tini", "--", "/home/claude/entrypoint.sh"]
