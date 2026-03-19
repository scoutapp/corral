# Sandclaude

A powerful Docker sandbox for running Claude Code in dangerous mode with network firewall protection and GitHub issue monitoring. Claude automatically works on GitHub issues as they're created.

Based on [sandclaude](https://github.com/binwiederhier/sandclauge) and [Claude Code's devcontainer](https://github.com/anthropics/claude-code/tree/main/.devcontainer).

## ⚠️ Warning

This runs Claude Code in **dangerous mode** where it can execute commands without asking for permission. Network firewall restricts outbound connections to approved domains only.

## Quick Start

```bash
# Clone and setup
git clone https://github.com/yourname/sandclaude.git
cd sandclaude

# Initialize project
bash sandclaude init myapp

# Start Claude with GitHub monitoring
bash sandclaude start myapp

# Copy to another project
bash sandclaude copy ~/my-project
```

## Features

- **Network Firewall**: iptables allowlist restricts outbound connections
- **GitHub Integration**: Auto-monitors and works on new issues every 60s
- **Secure Credentials**: Per-project credentials, never exposed
- **Claude Skill**: SKILL.md teaches Claude the firewall rules
- **Multi-Project**: Separate credentials and workspaces per project

## Prerequisites

- Docker
- Claude Code credentials (`claude` to authenticate)
- Optional: `gh` CLI (for GitHub issue monitoring)
- Optional: AWS credentials in `~/.aws/` (for AWS access)

## Usage

### Commands

| Command | Description |
|---------|-------------|
| `sandclaude init [project]` | Initialize new project with credentials |
| `sandclaude start [project]` | Start Claude Code for a project |
| `sandclaude copy <target>` | Copy sandclaude files to another directory |
| `sandclaude list` | List all configured projects |
| `sandclaude shell [project]` | Open bash shell in container |
| `sandclaude rebuild` | Force rebuild Docker image |
| `sandclaude help` | Show help message |

### Workflow

#### 1. Initialize a Project

```bash
bash sandclaude init myapp
```

This will prompt you for:
- **Enable GitHub issue monitoring?** (optional): If yes, provide `owner/repo`
- **Mount AWS credentials?** (optional): If yes, mounts `~/.aws` directory
- **Workspace directory**: Where your code lives (default: `~/projects/myapp`)

Configuration stored in `~/.config/sandclaude/projects/myapp/config/`

**Authentication:**
- GitHub: Uses host's `gh` CLI (mount `~/.config/gh`)
- AWS: Mounts `~/.aws/credentials` and `~/.aws/config` read-only

#### 2. Start Working

```bash
bash sandclaude start myapp
```

This starts:
- Docker container with firewall active
- Claude Code in dangerous mode
- GitHub issue monitor (if enabled, checks every 60 seconds)
- AWS credentials (if enabled, mounted at `~/.aws`)

#### 3. Optional Features

**GitHub Issue Monitoring** (if enabled):
- Checks every 60s for new unassigned issues
- Auto-assigns to bot
- Generates prompt with issue details
- Adds comment tracking progress

**AWS Integration** (if enabled):
- Mounts `~/.aws` read-only
- Works with STS temporary credentials
- No credentials stored in sandclaude config

#### 4. Managing Multiple Projects

```bash
# List all projects
bash sandclaude list

# Switch between projects
bash sandclaude start project-a
bash sandclaude start project-b

# Each has separate credentials and workspace
```

## How It Works

### Credential Security

### GitHub Authentication

Uses host's `gh` CLI for authentication:

1. **Host gh CLI** authenticated via `gh auth login`
2. **Mounted read-only** from `~/.config/gh` into container
3. **No API tokens** stored in sandclaude config
4. **Credentials never exposed** - handled entirely by gh CLI

The Python launcher uses `gh issue list`, `gh issue edit`, etc. commands which use the mounted authentication automatically.

### The Skill System

`skill/SKILL.md` is automatically mounted and teaches Claude:

- **Firewall architecture**: How iptables and ipset restrict connections
- **Allowed domains**: Pre-configured registries and services
- **How to request access**: Commands for approval and management
- **Credential safety**: Rules about never exposing mounted secrets
- **GitHub workflow**: How issue monitoring works
- **Troubleshooting**: Diagnose connection and firewall issues

Claude loads this on startup and knows the environment without being told.

## What's Included

The Docker image includes:

- Ubuntu 24.04
- Node.js 22
- Python 3
- GitHub CLI (`gh`)
- Standard build tools (make, gcc, etc.)
- **Firewall tools**: iptables, ipset, dnsutils, aggregate

## Firewall Management

### Interactive Approval of Blocked Connections

When Claude Code tries to access a blocked domain, you can approve it interactively:

```bash
# Inside the container, run:
firewall-helper.sh monitor
```

This will watch for blocked connections and prompt you to:
- **Allow permanently**: Add the domain to the firewall configuration
- **Keep blocking**: Reject the connection
- **Allow once**: (Future feature) Allow for current session only

### Manual Domain Management

```bash
# List all allowed domains
firewall-helper.sh list

# Add a domain to the allowed list
firewall-helper.sh add example.com

# Remove a domain from the allowed list
firewall-helper.sh remove example.com

# Clear the blocked connections cache (to see prompts again)
firewall-helper.sh clear-cache
```

### Firewall Configuration File

The firewall uses a simple text file for domain configuration:

**Location**: `/home/claude/.firewall/allowed-domains.txt`

You can edit this file directly and reload the firewall:

```bash
# Edit the allowed domains file
vim /home/claude/.firewall/allowed-domains.txt

# Reload firewall configuration
sudo /usr/local/bin/init-firewall.sh
```

### Default Allowed Domains

The firewall comes pre-configured with access to:

**Claude Code & Development**:
- `api.anthropic.com` - Claude API
- `registry.npmjs.org` - npm packages
- `registry.yarnpkg.com` - Yarn packages
- `github.com` and GitHub API/web/git ranges
- `marketplace.visualstudio.com` - VS Code extensions

**Go Development**:
- `proxy.golang.org` - Go module proxy
- `sum.golang.org` - Go checksum database
- `golang.org` - Go official site

**Popular CDNs**:
- `cdn.jsdelivr.net`
- `unpkg.com`

**Container Registries**:
- `registry.hub.docker.com`
- `ghcr.io`
- `quay.io`

### Disable Interactive Mode

If you prefer not to be prompted for blocked connections:

```bash
echo 'disabled' > /home/claude/.firewall/interactive-mode
```

## How It Works

1. Checks for Claude credentials at `~/.claude/.credentials.json`
2. Builds Docker image on first run (matching your host UID/GID)
3. Mounts workspace and credentials into container
4. **Initializes network firewall** with pre-configured allowed domains
5. Runs Claude with `--dangerously-skip-permissions`
6. Monitors blocked connections (if interactive mode enabled)
7. Drops to bash shell when Claude exits

### Firewall Technical Details

The firewall uses **iptables** and **ipset** to:

1. Resolve allowed domains to IP addresses
2. Create an IP set containing all allowed IPs and CIDR ranges
3. Block all outbound traffic except:
   - DNS (port 53)
   - SSH (port 22)
   - Localhost
   - Host network
   - IPs in the allowed set
4. Log blocked connections for interactive approval

The firewall is initialized on container startup via the `postStartCommand` in `.devcontainer/devcontainer.json`.

## Customization

Edit the `Dockerfile` to add more tools:

```dockerfile
# Add Go
RUN curl -fsSL https://go.dev/dl/go1.23.0.linux-amd64.tar.gz | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# Add Rust
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
```

Then rebuild:

```bash
dangerous-claude -b
```

## Security Notes

- Container runs as non-root user matching your host UID/GID
- Container is isolated from host (except mounted workspace)
- Container is ephemeral (`--rm` flag removes it after exit)
- Claude has full access to the mounted workspace
- GitHub credentials are mounted read-only
- **Network firewall restricts outbound connections** to approved domains only
- Firewall configuration persists across container restarts (via Docker volumes)

## Adding to Your Project

### Quick Copy

```bash
# Automatically creates .devcontainer/
bash sandclaude copy ~/my-project

# Result:
# ~/my-project/.devcontainer/
#   ├── Dockerfile
#   ├── devcontainer.json
#   ├── init-firewall.sh
#   ├── firewall-helper.sh
#   ├── launcher.py
#   ├── requirements.txt
#   └── skill/SKILL.md
```

Then:
```bash
code ~/my-project  # Opens VS Code
# Click "Reopen in Container"
```

### From Existing Repo

```bash
cd ~/my-project
git clone https://github.com/yourname/sandclaude .devcontainer
rm -rf .devcontainer/.git

# Open in VS Code
code .
```

### Validate Skill Loading

Inside container:
```bash
# Check if skill file exists
ls -la /home/claude/.claude/skills/sandclaude.md

# Ask Claude
claude --prompt "What firewall domains are pre-configured?"
# If skill loaded, Claude will list npm, GitHub, Go proxy, etc.
```

## Troubleshooting

### Connection Refused Errors

If Claude Code or your tools are getting connection refused errors:

1. Run `firewall-helper.sh monitor` to see blocked connections
2. Approve domains as they appear
3. Or manually add them: `firewall-helper.sh add example.com`

### Firewall Not Working

Check that the container has the required capabilities:
```bash
# Inside container
sudo /usr/local/bin/init-firewall.sh
```

If you get permission errors, ensure `--cap-add=NET_ADMIN` and `--cap-add=NET_RAW` are set.

### Reset Firewall Configuration

```bash
# Remove the firewall configuration directory
rm -rf /home/claude/.firewall

# Restart the firewall
sudo /usr/local/bin/init-firewall.sh
```

## License

Public domain / use freely
