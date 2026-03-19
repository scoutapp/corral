FROM ubuntu:24.04

# Install base tools and firewall dependencies
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
    iptables \
    ipset \
    iproute2 \
    dnsutils \
    aggregate \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 22
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Python 3
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
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

# Copy and set up firewall scripts
COPY init-firewall.sh /usr/local/bin/
COPY firewall-helper.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/init-firewall.sh && \
    chmod +x /usr/local/bin/firewall-helper.sh && \
    echo "claude ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh" >> /etc/sudoers.d/claude && \
    echo "claude ALL=(root) NOPASSWD: /usr/local/bin/firewall-helper.sh" >> /etc/sudoers.d/claude && \
    echo "claude ALL=(root) NOPASSWD: /usr/bin/dmesg" >> /etc/sudoers.d/claude && \
    chmod 0440 /etc/sudoers.d/claude

USER claude
ENV HOME=/home/claude
ENV TERM=xterm-256color

# Install Claude Code
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH="/home/claude/.claude/bin:/home/claude/.local/bin:${PATH}"

# Install Python dependencies for launcher
COPY requirements.txt /tmp/requirements.txt
RUN pip3 install --no-cache-dir -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

# Copy launcher and entrypoint
COPY --chown=claude:claude launcher.py /home/claude/launcher.py
COPY --chown=claude:claude entrypoint.sh /home/claude/entrypoint.sh
RUN chmod +x /home/claude/launcher.py /home/claude/entrypoint.sh

ENTRYPOINT ["/home/claude/entrypoint.sh"]
