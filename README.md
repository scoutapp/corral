# Sandclaude

Docker sandbox for running Claude Code in dangerous mode with network firewall protection and GitHub issue monitoring.

## ⚠️ Warning

Runs Claude Code in **dangerous mode** (no permission prompts). Network firewall restricts outbound connections to approved domains only.

Credential proxy requires `mitmproxy` — install via `brew install mitmproxy` or https://www.mitmproxy.org/

## Quick Start

```bash
# Clone into your project's .devcontainer directory
cd ~/my-project
git clone https://github.com/scoutapp/sandclaude.git .devcontainer
cd .devcontainer

# Build the sandclaude binary
go build -o sandclaude main.go

# Initialize (creates ./project/ and encrypts allowlist automatically)
./sandclaude init

# Start Claude
./sandclaude start
```

## Features

- **Network Firewall**: Domain allowlist proxy restricts outbound connections
- **GitHub Integration**: Auto-monitors and works on issues every 60s (optional)
- **AWS Support**: Reads `~/.aws` credentials, passes as env vars (never volume-mounted)
- **No credential mounts**: Claude credentials, gh tokens, and AWS keys are passed as env vars only
- **Claude Skill**: Teaches Claude the firewall rules automatically

## Prerequisites

- Go 1.21+ (to build the binary)
- Docker
- Claude Code credentials (`claude` to authenticate)
- Optional: `gh` CLI (GitHub monitoring), AWS credentials, mitmproxy (for proxy mode)

## Commands

| Command | Description |
|---------|-------------|
| `./sandclaude init` | Initialize project (creates `./project/` and encrypts allowlist) |
| `./sandclaude start` | Start Claude Code |
| `./sandclaude start --disable-firewall` | Start without firewall (unrestricted network) |
| `./sandclaude list` | Show project configuration |
| `./sandclaude remove` | Remove project and encrypted allowlist |
| `./sandclaude firewall-reload` | Re-encrypt allowlist and SIGHUP running proxy |
| `./sandclaude firewall-monitor` | Tail the allowlist proxy log |
| `./sandclaude shell` | Open debug shell in container |
| `./sandclaude copy <target>` | Copy files to .devcontainer/ |
| `./sandclaude rebuild` | Force rebuild Docker image |
| `./sandclaude help` | Show help |

## Usage

### Initialize Project

```bash
./sandclaude init
```

Creates `./project/` in this directory. Prompts for:
- **GitHub monitoring?** (optional): Provide `owner/repo`
- **AWS credentials?** (optional): Reads from `~/.aws`, passes as env vars
- **Credential proxy?** (optional): Enable mitmproxy to hide real credentials from Claude
- **Workspace directory**: Defaults to parent directory (resolved absolute path)

Config stored in `./project/config/`. Add `./project/` to `.gitignore`.

### Start Working

```bash
export ALLOWLIST_KEY=<your-passphrase>  # required
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
- Reads `~/.aws/credentials` and `~/.aws/config` on the host
- Passes `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION` as env vars
- Works with STS temporary credentials
- The `~/.aws` directory is never volume-mounted into the container

### Credential Proxy

When enabled during `init`, `sandclaude` runs `mitmweb` on the host and routes all container traffic through it. This prevents Claude from seeing or exfiltrating real credentials.

**How it works:**
1. `sandclaude start` launches `mitmweb` on `0.0.0.0:8080` (host)
2. Container is started with `HTTP_PROXY`/`HTTPS_PROXY` pointing to `host.docker.internal:8080`
3. Claude receives a dummy OAuth token inside the container
4. `mitmweb` intercepts outbound requests and injects the real credentials from `proxy-credentials.json`
5. Proxy stops automatically when the container exits

**Setup:**

Get your Claude OAuth token:
```bash
claude setup-token
```

Configure credentials in `./project/proxy-credentials.json`:

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
  }
}
```

**mitmproxy certificate trust** (required on first run):

The mitmproxy CA cert is generated at `~/.mitmproxy/mitmproxy-ca-cert.pem` on first launch. It is mounted read-only into the container automatically. If you see TLS errors, run mitmweb once standalone to generate the cert, then rebuild:
```bash
mitmweb --listen-port 8080
# Ctrl+C once cert is generated (~/.mitmproxy/ created)
./sandclaude rebuild
```

**Proxy logs** are written to `logs/mitm.log` in the directory where you run `sandclaude start`. View the mitmweb UI at `http://127.0.0.1:8081` while the session is running.

## Firewall Management

The firewall is implemented as a Go HTTP CONNECT proxy (`allowlist-proxy`) running inside the container on `127.0.0.1:3128`. All of Claude's traffic is routed through it via `HTTP_PROXY`/`HTTPS_PROXY`. Connections to domains not in the allowlist are rejected.

### Allowed Domains File

The allowlist lives at `allowlist-proxy/allowed-domains.txt` (plaintext). When you modify it, run `firewall-reload` to re-encrypt and reload:

```bash
echo 'example.com' >> allowlist-proxy/allowed-domains.txt
./sandclaude firewall-reload
```

This command:
1. Reads the encryption key from `project/.allowlist-key`
2. Re-encrypts `allowed-domains.txt` → `allowed-domains.txt.enc`
3. If a container is running, sends SIGHUP to reload the allowlist without restart

**Note**: The encryption key is auto-generated during `init` and stored in `project/.allowlist-key` (never committed).

### Monitor Proxy Log

```bash
./sandclaude firewall-monitor
```

Tails the allowlist proxy log inside the running container. Blocked connections appear as `BLOCKED` lines with the destination host.

### Pre-configured Domains

- **Claude/Anthropic**: `api.anthropic.com`, `statsig.anthropic.com`, `statsig.com`, `sentry.io`
- **npm / Node**: `registry.npmjs.org`, `registry.yarnpkg.com`, `npm.pkg.github.com`
- **Go**: `proxy.golang.org`, `sum.golang.org`, `golang.org`
- **GitHub**: `api.github.com`, `raw.githubusercontent.com`, `github.com`
- **CDNs**: `cdn.jsdelivr.net`, `storage.googleapis.com`

## How It Works

1. Builds Docker image (matches host UID/GID)
2. Mounts workspace and credentials
3. Initializes iptables firewall with domain allowlist
4. Runs Claude with `--dangerously-skip-permissions`
5. Monitors GitHub issues in background (if enabled)
6. Logs blocked connections for approval

**Firewall**: A Go HTTP CONNECT proxy (`allowlist-proxy`) runs inside the container on `127.0.0.1:3128`. Claude's `HTTP_PROXY`/`HTTPS_PROXY` env vars point to it. Connections to domains not in the allowlist are rejected with a `403`. The allowlist is stored encrypted at `allowlist-proxy/allowed-domains.txt.enc` (bind-mounted into the container) and supports hot-reload via `sandclaude reload-firewall`. Logs go to `logs/proxy.log` inside the container.

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

**Domain blocked (connection refused / 403):**
```bash
# Check what's being blocked
./sandclaude firewall-monitor myapp

# Add a domain and hot-reload (no container restart needed)
echo 'example.com' >> allowlist-proxy/allowed-domains.txt
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude reload-firewall
```

**Proxy not starting:**
```bash
# Check proxy log inside container
./sandclaude shell myapp
cat logs/proxy.log
```

**Disable firewall for debugging:**
```bash
./sandclaude start --disable-firewall
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
