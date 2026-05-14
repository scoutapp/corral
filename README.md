# Sandclaude

Docker sandbox for running Claude Code in dangerous mode with network firewall protection.

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
```

**We strongly recommend enabling the credential proxy during `init`.** It prevents Claude from seeing or exfiltrating real credentials by intercepting and injecting them at the proxy level. After init, run:

```bash
./sandclaude populate-proxy-credentials
```

Currently, this will populate the proxy-credentials.json with your Claude and Github tokens.

```bash
# Start Claude
./sandclaude start
```

### New Projects: Auto-Discover Required Domains

When working with a new project that downloads dependencies from various sources (npm packages, go modules, pip packages, CDNs, etc.), use **passthrough mode** to automatically discover what domains are needed:

```bash
# Start in passthrough mode - allows all traffic and logs unknown domains
./sandclaude start --passthrough-firewall-and-write

# Work on your project normally:
# - Install dependencies (npm install, go get, pip install, etc.)
# - Download assets from CDNs
# - Clone git repos
# - Run build tools
# All accessed domains are automatically written to allowlist-proxy/allowed-domains.txt

# When done, lock down the firewall with the discovered domains:
./sandclaude firewall-reload

# Restart with firewall enforced:
./sandclaude start
```

This workflow is ideal for:
- **New projects** with unknown dependencies
- **Legacy projects** where the full dependency graph isn't documented
- **Build pipelines** that fetch from multiple package registries
- **CDN-heavy frontends** that load assets from various sources

## Features

- **Network Firewall**: Domain allowlist proxy restricts outbound connections
- **Credential Proxy**: Hides real credentials from Claude using mitmproxy
- **No credential mounts**: Claude credentials and gh tokens are passed as env vars only
- **Claude Skill**: Teaches Claude the firewall rules automatically

## Prerequisites

- Go 1.21+ (to build the binary)
- Docker
- Claude Code installed and authenticated on the host (`claude` to sign in first)
- Optional: `gh` CLI, mitmproxy (for proxy mode — strongly recommended)

**Important**: You must authenticate Claude Code on your host machine before running `sandclaude`. Run `claude` to sign in. This creates `~/.claude.json` with your session state (does NOT contain auth credentials), which is mounted into the container. If you skip this step, Claude will prompt for authentication inside the container.

## Commands

| Command | Description |
|---------|-------------|
| `./sandclaude init` | Initialize project (creates `./project/` and encrypts allowlist) |
| `./sandclaude start` | Start Claude Code |
| `./sandclaude start --disable-firewall` | Start without firewall (unrestricted network) |
| `./sandclaude start --passthrough-firewall-and-write` | Allow all traffic, log unknown domains to `allowed-domains.txt` |
| `./sandclaude list` | Show project configuration |
| `./sandclaude remove` | Remove project and encrypted allowlist |
| `./sandclaude firewall-reload` | Re-encrypt allowlist and SIGHUP running proxy |
| `./sandclaude firewall-monitor` | Tail the allowlist proxy log |
| `./sandclaude shell` | Open debug shell in container |
| `./sandclaude copy <target>` | Copy files to .devcontainer/ |
| `./sandclaude rebuild` | Force rebuild Docker image |
| `./sandclaude rebuild --destroy` | Rebuild from scratch (removes existing image/container first) |
| `./sandclaude populate-proxy-credentials` | Interactively populate proxy-credentials.json |
| `./sandclaude help` | Show help |

## Usage

### Initialize Project

```bash
./sandclaude init
```

Creates `./project/` in this directory. Prompts for:
- **Credential proxy?** (optional, strongly recommended): Enable mitmproxy to hide real credentials from Claude
- **Workspace directory**: Defaults to parent directory (resolved absolute path)

Config stored in `./project/config/`. Add `./project/` to `.gitignore`.

### Start Working

```bash
./sandclaude start
```

Starts:
- Credential proxy (automatically if configured during init)
- Docker container with firewall
- Claude Code in dangerous mode

### Credential Proxy

When enabled during `init`, `sandclaude` runs `mitmweb` on the host and routes all container traffic through it. This prevents Claude from seeing or exfiltrating real credentials.

**How it works:**
1. `sandclaude start` launches `mitmweb` on `0.0.0.0:8080` (host)
2. Container is started with `HTTP_PROXY`/`HTTPS_PROXY` pointing to `host.docker.internal:8080`
3. Claude receives a dummy OAuth token inside the container
4. `mitmweb` intercepts outbound requests and injects the real credentials from `proxy-credentials.json`
5. Proxy stops automatically when the container exits

**Setup:**

```bash
./sandclaude populate-proxy-credentials
```

This interactively populates `./project/proxy-credentials.json` using `claude setup-token` and `gh auth token`. You can also configure it manually:

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
    "value": "token gho_real_token_here"
  }
}
```

Each entry supports two injection modes:

| Key | Description |
|-----|-------------|
| `header` | Injects the value as an HTTP request header (e.g. `Authorization`) |
| `url_param` | Injects the value as a URL query parameter (e.g. `?api_key=...`) |

Example using `url_param`:

```json
{
  "api.example.com": {
    "url_param": "api_key",
    "value": "secret123"
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

**Encryption Key**: The encryption key is auto-generated during `init` and stored in `project/.allowlist-key` (never committed). It is automatically read from this file by `sandclaude start` and `firewall-reload` - you don't need to set anything manually.

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

## Docker-in-Docker (DinD)

When enabled, sandclaude runs a private Docker daemon (`dockerd`) inside the container. Claude can start, exec into, and manage inner containers — for example, spinning up a Rails app, Django server, Postgres database, or any other service.

**All inner container network egress is automatically routed through the same allowlist proxy.** Inner containers cannot reach unapproved domains regardless of what runs inside them.

### Setup

During `sandclaude init`, answer yes to the DinD prompt and enter any ports you want accessible from your host:

```bash
./sandclaude init myapp
# ...
# Enable Docker-in-Docker (Claude can start inner containers)? [y/N]: y
# Port mappings to expose to host (e.g. 3000:3000,8000:8000, blank for none): 3000:3000
```

Port mappings can also be edited later:

```bash
echo "5432:5432" >> ~/.config/sandclaude/projects/myapp/config/dind_ports
# Then restart: ./sandclaude start myapp
```

### How It Works

```
Your host (Mac)
  └── Docker Desktop VM
        └── sandclaude container (--privileged)
              ├── allowlist-proxy (:3128)
              ├── dockerd (inner daemon, unix:///var/run/dind/docker.sock)
              │     ├── rails container (172.18.x.x)
              │     ├── postgres container (172.18.x.x)
              │     └── ...
              └── iptables PREROUTING: 172.18.0.0/16 TCP -> :3128
```

Inner containers sit on a `172.18.0.0/16` bridge. Any outbound TCP they attempt is intercepted by a PREROUTING REDIRECT rule and handed to the allowlist proxy. The proxy checks the domain against `allowed-domains.txt` and either forwards or blocks it.

`DOCKER_HOST` inside the sandclaude container is automatically set to `unix:///var/run/dind/docker.sock`, so all `docker` commands Claude runs go to the inner daemon — not the host Docker socket (which is never mounted).

### Troubleshooting DinD

**overlay2 fails ("operation not permitted"):**
```bash
# Set storage driver to vfs (slower but always works):
./sandclaude start myapp --dind-storage-driver=vfs
# Or set env var before starting:
DIND_STORAGE_DRIVER=vfs ./sandclaude start myapp
```

**Check inner dockerd logs:**
```bash
./sandclaude shell myapp
cat .firewall/dockerd.log
```

**Inner container can't reach a domain:**
```bash
# Add domain to allowlist and reload proxy:
echo 'rubygems.org' >> allowlist-proxy/allowed-domains.txt
./sandclaude firewall-reload
```

**Skip DinD for a single run:**
```bash
./sandclaude start myapp --disable-dind
```

## How It Works

1. Builds Docker image (matches host UID/GID)
2. Mounts workspace and credentials
3. Initializes iptables firewall with domain allowlist
4. Runs Claude with `--dangerously-skip-permissions`
5. Logs blocked connections for approval

**Firewall**: A Go HTTP CONNECT proxy (`allowlist-proxy`) runs inside the container on `127.0.0.1:3128`. Claude's `HTTP_PROXY`/`HTTPS_PROXY` env vars point to it. Connections to domains not in the allowlist are rejected with a `403`. The allowlist is stored encrypted at `allowlist-proxy/allowed-domains.txt.enc` (bind-mounted into the container) and supports hot-reload via `sandclaude firewall-reload`. Logs go to `logs/proxy.log` inside the container.

**Skill System**: `.claude/skills/environment/SKILL.md` auto-mounted into the container teaches Claude:
- Firewall architecture and allowed domains
- How to request domain access
- Troubleshooting steps

## What's Included

- Ubuntu 24.04, Node.js 22, Python 3, gh CLI, uv
- Build tools (make, gcc, etc.)
- Firewall tools (iptables, ipset, dnsutils)

## Troubleshooting

**Domain blocked (connection refused / 403):**
```bash
# Check what's being blocked
./sandclaude firewall-monitor

# Add a domain and hot-reload (no container restart needed)
echo 'example.com' >> allowlist-proxy/allowed-domains.txt
./sandclaude firewall-reload
```

**Proxy not starting:**
```bash
# Check proxy log inside container
./sandclaude shell
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
- Credential proxy prevents Claude from seeing real credentials

## License

Public domain / use freely
