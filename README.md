# Sandclaude

Docker sandbox for running Claude Code in dangerous mode with network firewall protection and GitHub issue monitoring.

## ⚠️ Warning

Runs Claude Code in **dangerous mode** (no permission prompts). Network firewall restricts outbound connections to approved domains only.

## Quick Start

```bash
git clone https://github.com/yourname/sandclaude.git
cd sandclaude

bash sandclaude init myapp
bash sandclaude start myapp
```

## Features

- **Network Firewall**: iptables allowlist restricts outbound connections
- **GitHub Integration**: Auto-monitors and works on issues every 60s (optional)
- **AWS Support**: Mounts `~/.aws` credentials read-only (optional)
- **Multi-Project**: Separate configs and workspaces per project
- **Claude Skill**: Teaches Claude the firewall rules automatically

## Prerequisites

- Docker
- Claude Code credentials (`claude` to authenticate)
- Optional: `gh` CLI (GitHub monitoring), AWS credentials

## Commands

| Command | Description |
|---------|-------------|
| `bash sandclaude init [project]` | Initialize project |
| `bash sandclaude start [project]` | Start Claude Code |
| `bash sandclaude copy <target>` | Copy to .devcontainer/ |
| `bash sandclaude list` | List projects |
| `bash sandclaude shell [project]` | Debug shell |

## Usage

### Initialize Project

```bash
bash sandclaude init myapp
```

Prompts for:
- **GitHub monitoring?** (optional): Provide `owner/repo`
- **AWS credentials?** (optional): Mounts `~/.aws`
- **Workspace directory**: Default `~/projects/myapp`

Config stored in `~/.config/sandclaude/projects/myapp/config/`

### Start Working

```bash
bash sandclaude start myapp
```

Starts:
- Docker container with firewall
- Claude Code in dangerous mode
- GitHub issue monitor (if enabled)
- AWS credentials (if enabled)

### GitHub Integration

When enabled:
- Checks every 60s for unassigned issues
- Auto-assigns to bot
- Generates prompt with issue details
- Uses host's `gh` CLI (no tokens stored)

### AWS Integration

When enabled:
- Mounts `~/.aws` read-only
- Works with STS temporary credentials
- Sets AWS env vars automatically

## Firewall Management

### Interactive Approval

```bash
firewall-helper.sh monitor
```

Prompts to allow/deny blocked connections permanently.

### Manual Management

```bash
firewall-helper.sh list                    # List allowed domains
firewall-helper.sh add example.com         # Add domain
firewall-helper.sh remove example.com      # Remove domain
```

### Configuration File

Edit `/home/claude/.firewall/allowed-domains.txt` and reload:

```bash
sudo /usr/local/bin/init-firewall.sh
```

### Pre-configured Domains

- **Claude/Dev**: `api.anthropic.com`, `registry.npmjs.org`, `github.com`, VS Code marketplace
- **Go**: `proxy.golang.org`, `sum.golang.org`
- **CDNs**: `cdn.jsdelivr.net`, `unpkg.com`
- **Registries**: Docker Hub, `ghcr.io`, `quay.io`

## How It Works

1. Builds Docker image (matches host UID/GID)
2. Mounts workspace and credentials
3. Initializes iptables firewall with domain allowlist
4. Runs Claude with `--dangerously-skip-permissions`
5. Monitors GitHub issues in background (if enabled)
6. Logs blocked connections for approval

**Firewall**: Uses iptables + ipset to block all outbound traffic except DNS, SSH, localhost, and allowed IPs. Domains resolved to IPs at startup.

**Skill System**: `skill/SKILL.md` auto-mounted at `/home/claude/.claude/skills/sandclaude.md` teaches Claude:
- Firewall architecture and allowed domains
- How to request domain access
- GitHub and AWS workflow
- Troubleshooting steps

## Adding to Your Project

### Quick Copy

```bash
bash sandclaude copy ~/my-project
code ~/my-project  # Click "Reopen in Container"
```

Creates `~/my-project/.devcontainer/` with all files.

### Clone as .devcontainer

```bash
cd ~/my-project
git clone https://github.com/yourname/sandclaude .devcontainer
rm -rf .devcontainer/.git
code .
```

### Validate Skill Loading

```bash
ls /home/claude/.claude/skills/sandclaude.md
claude --prompt "What firewall domains are allowed?"
```

## What's Included

- Ubuntu 24.04, Node.js 22, Python 3, gh CLI
- Build tools (make, gcc, etc.)
- Firewall tools (iptables, ipset, dnsutils)

## Troubleshooting

**Connection refused:**
```bash
firewall-helper.sh monitor  # Approve interactively
firewall-helper.sh add domain.com  # Or add manually
```

**Firewall not working:**
```bash
sudo /usr/local/bin/init-firewall.sh  # Reload
```

Ensure `--cap-add=NET_ADMIN` and `--cap-add=NET_RAW` are set.

**Reset firewall:**
```bash
rm -rf /home/claude/.firewall
sudo /usr/local/bin/init-firewall.sh
```

## Security

- Container runs as non-root (matches host UID/GID)
- Ephemeral container (`--rm` flag)
- Workspace mounted read-write, credentials read-only
- Network firewall blocks unapproved domains
- GitHub auth via mounted `gh` CLI (no tokens stored)
- AWS credentials mounted read-only from `~/.aws`

## License

Public domain / use freely
