# Sandclaude

Docker sandbox for running Claude Code in dangerous mode with network firewall protection and GitHub issue monitoring.

## ⚠️ Warning

Runs Claude Code in **dangerous mode** (no permission prompts). Network firewall restricts outbound connections to approved domains only.

Proxy requires `mitmproxy` https://www.mitmproxy.org/

## Quick Start

```bash
# Clone into your project's .devcontainer directory
cd ~/my-project
git clone https://github.com/scoutapp/sandclaude.git .devcontainer
cd .devcontainer

# Build the sandclaude binary
go build -o sandclaude main.go

# Initialize a project (prompts for configuration)
./sandclaude init myapp

# Start Claude (proxy starts automatically if configured)
# Will need to set proxy configuration values. See Credential Proxy below.
./sandclaude start myapp
```

## Features

- **Network Firewall**: iptables allowlist restricts outbound connections
- **GitHub Integration**: Auto-monitors and works on issues every 60s (optional)
- **AWS Support**: Mounts `~/.aws` credentials read-only (optional)
- **Multi-Project**: Separate configs and workspaces per project
- **Claude Skill**: Teaches Claude the firewall rules automatically

## Prerequisites

- Go 1.21+ (to build the binary)
- Docker
- Claude Code credentials (`claude` to authenticate)
- Optional: `gh` CLI (GitHub monitoring), AWS credentials, mitmproxy (for proxy mode)

## Commands

| Command | Description |
|---------|-------------|
| `./sandclaude init [project]` | Initialize project (prompts if no arg) |
| `./sandclaude start [project]` | Start Claude Code (prompts if no arg) |
| `./sandclaude list` | List configured projects |
| `./sandclaude remove <project>` | Remove a project |
| `./sandclaude shell [project]` | Open debug shell in container |
| `./sandclaude copy <target>` | Copy files to .devcontainer/ |
| `./sandclaude rebuild` | Force rebuild Docker image |
| `./sandclaude help` | Show help |

## Usage

### Initialize Project

```bash
./sandclaude init myapp
# Or run without argument to use current directory name:
./sandclaude init
```

Prompts for:
- **Project name**: Defaults to current directory name
- **GitHub monitoring?** (optional): Provide `owner/repo`
- **AWS credentials?** (optional): Mounts `~/.aws`
- **Credential proxy?** (optional): Enable proxy to hide real credentials from Claude
- **Workspace directory**: Defaults to parent directory (resolved absolute path)

Config stored in `~/.config/sandclaude/projects/myapp/config/`

### Start Working

```bash
./sandclaude start myapp
# Or run without argument to use current directory name:
./sandclaude start
```

Starts:
- Credential proxy (automatically if configured during init)
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

### Credential Proxy

When enabled during `init`:
- Hides real credentials from Claude using mitmproxy
- Claude uses dummy credentials inside container
- Proxy intercepts requests and injects real credentials
- Prevents credential exfiltration
- Proxy starts automatically when running `sandclaude start`
- Proxy logs written to `mitm.log`
- Configure credentials in `~/.config/sandclaude/proxy-credentials.json`:
- **CLAUDE TOKEN** To get the claude code oauth token run `claude setup-token` 

```json
{
  "api.anthropic.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
  "platform.claude.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
  "mcp-proxy.anthropic.com": {
    "header": "Authorization",
    "value": "Bearer sk-ant-oat01-..."
  },
   "api.github.com": {
    "header": "Authorization",
    "value": "Bearer ghp_real_token_here"
  },
}
```

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
./sandclaude copy ~/my-project
code ~/my-project  # Click "Reopen in Container"
```

Creates `~/my-project/.devcontainer/` with all files.

**Note**: Python launcher uses [uv](https://docs.astral.sh/uv/) for dependency management - no pip or requirements.txt needed.

### Validate Skill Loading

```bash
ls /home/claude/.claude/skills/sandclaude.md
claude --prompt "What firewall domains are allowed?"
```

## What's Included

- Ubuntu 24.04, Node.js 22, Python 3, gh CLI, uv
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
