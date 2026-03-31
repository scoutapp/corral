# ── Stage 1: build allowlist-proxy ───────────────────────────────────────────
FROM golang:1.22-alpine AS proxy-builder

WORKDIR /build
COPY allowlist-proxy/ .
# CGO_ENABLED=0 produces a fully static binary — no libc needed in final image
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o allowlist-proxy main.go

# ── Stage 2: runtime image ────────────────────────────────────────────────────
FROM --platform=linux/amd64 ubuntu:24.04

# Copy the static proxy binary from the builder stage
COPY --from=proxy-builder /build/allowlist-proxy /usr/local/bin/allowlist-proxy

# Install Google Chrome and matching ChromeDriver
# (chromium-browser on Ubuntu 24.04 is a snap stub that doesn't work in Docker)
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    wget \
    curl \
    unzip \
    gnupg \
    ca-certificates \
    && wget -q -O - https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg \
    && echo "deb [arch=amd64 signed-by=/usr/share/keyrings/google-chrome.gpg] http://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends google-chrome-stable \
    && CHROME_VERSION=$(google-chrome --version | grep -oP '\d+' | head -1) \
    && CHROMEDRIVER_VERSION=$(curl -s "https://googlechromelabs.github.io/chrome-for-testing/LATEST_RELEASE_${CHROME_VERSION}") \
    && wget -q "https://storage.googleapis.com/chrome-for-testing-public/${CHROMEDRIVER_VERSION}/linux64/chromedriver-linux64.zip" \
    && unzip chromedriver-linux64.zip \
    && mv chromedriver-linux64/chromedriver /usr/local/bin/chromedriver \
    && chmod +x /usr/local/bin/chromedriver \
    && rm -rf chromedriver-linux64.zip chromedriver-linux64 \
    && rm -rf /var/lib/apt/lists/*

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
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 22
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Python 3, mitmproxy, and selenium
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
    python3-selenium \
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
ENV PATH="/home/claude/.claude/bin:/home/claude/.local/bin:${PATH}"

# Copy launcher, entrypoint, proxy addon, skill, and bin scripts
COPY --chown=claude:claude launcher.py /home/claude/launcher.py
COPY --chown=claude:claude entrypoint.sh /home/claude/entrypoint.sh
COPY --chown=claude:claude proxy-addon.py /usr/local/bin/proxy-addon.py
COPY --chown=claude:claude .claude/skills/ /home/claude/.claude/skills/
COPY --chown=claude:claude bin/ /home/claude/bin/
RUN chmod +x /home/claude/launcher.py /home/claude/entrypoint.sh /usr/local/bin/proxy-addon.py \
    /home/claude/bin/cert-injector

ENTRYPOINT ["/usr/bin/tini", "--", "/home/claude/entrypoint.sh"]
