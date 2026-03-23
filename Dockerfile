FROM ubuntu:24.04

# Install Go for building allowlist-proxy
RUN apt-get update && apt-get install -y --no-install-recommends golang-go && \
    rm -rf /var/lib/apt/lists/*

# Build allowlist-proxy
COPY allowlist-proxy/ /build/allowlist-proxy/
RUN cd /build/allowlist-proxy && go build -o /usr/local/bin/allowlist-proxy . && \
    rm -rf /build

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
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 22
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Python 3 and mitmproxy
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

# Create non-root user matching host UID/GID
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN groupadd -f -g ${GROUP_ID} claude && \
    useradd -m -u ${USER_ID} -g claude -o claude && \
    echo "claude ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/claude

# allowlist-proxy is already at /usr/local/bin/allowlist-proxy from the build stage
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

# Copy launcher, entrypoint, and proxy addon
COPY --chown=claude:claude launcher.py /home/claude/launcher.py
COPY --chown=claude:claude entrypoint.sh /home/claude/entrypoint.sh
COPY --chown=claude:claude proxy-addon.py /usr/local/bin/proxy-addon.py
RUN chmod +x /home/claude/launcher.py /home/claude/entrypoint.sh /usr/local/bin/proxy-addon.py

ENTRYPOINT ["/home/claude/entrypoint.sh"]
