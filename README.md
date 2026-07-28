# Sandclaude

Docker sandbox for running Claude Code in dangerous mode with network firewall protection.

## ⚠️ Warning

Runs Claude Code in **dangerous mode** (no permission prompts). Network firewall restricts outbound connections to approved domains only.

Credential proxy requires `mitmproxy` — install via `brew install mitmproxy` or https://www.mitmproxy.org/

## Quick Start

Install once, then use `sandclaude` in any project — no per-project clone, no `.devcontainer`.

```bash
# Clone the repo somewhere permanent and install
git clone https://github.com/scoutapp/sandclaude.git
cd sandclaude
./install.sh          # builds the binary -> /usr/local/bin, assets -> ~/.sandclaude/assets
```

`install.sh` puts the `sandclaude` binary on your `$PATH` and copies the Docker build
context (Dockerfile, entrypoint, launcher, allowlist proxy, etc.) into `~/.sandclaude/assets`.

```bash
# In any project you want to work on
cd ~/my-project
sandclaude init       # creates ./.sandclaude/ (config, allowlist, key) in this directory
```

**We strongly recommend enabling the credential proxy during `init`.** It prevents Claude
from seeing or exfiltrating real credentials by intercepting and injecting them at the proxy
level. Credentials are stored **globally** and reused across all projects:

```bash
sandclaude populate-proxy-credentials   # writes ~/.sandclaude/proxy-credentials.json
```

This populates the global credentials file with your Claude and GitHub tokens. To add a
project-specific override (takes precedence per-domain over the global file), run:

```bash
sandclaude populate-proxy-credentials --project   # writes ./.sandclaude/project/proxy-credentials.json
```

```bash
# Start Claude
sandclaude start
```

### Install locations & overrides

| Path | Contents |
|------|----------|
| `/usr/local/bin/sandclaude` | The CLI binary (override dir with `SANDCLAUDE_PREFIX`) |
| `~/.sandclaude/assets/` | Docker build context + support files (override with `SANDCLAUDE_HOME`) |
| `~/.sandclaude/proxy-credentials.json` | Global credentials (shared across projects) |
| `./.sandclaude/project/` | Per-project config, `.allowlist-key`, optional creds override |
| `./.sandclaude/allowed-domains.txt[.enc]` | Per-project firewall allowlist |

To reinstall after editing sources: re-run `./install.sh` (idempotent). See
[Developing sandclaude](#developing-sandclaude) for the local dev loop.

### New Projects: Auto-Discover Required Domains

When working with a new project that downloads dependencies from various sources (npm packages, go modules, pip packages, CDNs, etc.), use **passthrough mode** to automatically discover what domains are needed:

```bash
# Start in passthrough mode - allows all traffic and logs unknown domains
sandclaude start --passthrough-firewall-and-write

# Work on your project normally:
# - Install dependencies (npm install, go get, pip install, etc.)
# - Download assets from CDNs
# - Clone git repos
# - Run build tools
# All accessed domains are automatically written to ./.sandclaude/allowed-domains.txt

# When done, lock down the firewall with the discovered domains:
sandclaude firewall-reload

# Restart with firewall enforced:
sandclaude start
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

- Go 1.21+ (to build the binary via `install.sh`)
- Docker
- Claude Code installed and authenticated on the host (`claude` to sign in first)
- Optional: `gh` CLI, mitmproxy (for proxy mode — strongly recommended)
- Optional: `rsync` (used by `install.sh`; falls back to `cp` if absent)
- Optional: `tmux` (for `sandclaude dev`/`capture`/`send`/`attach`, and the dashboard's terminal tab)

**Important**: You must authenticate Claude Code on your host machine before running `sandclaude`. Run `claude` to sign in. This creates `~/.claude.json` with your session state (does NOT contain auth credentials), which is mounted into the container. If you skip this step, Claude will prompt for authentication inside the container.

## Commands

| Command | Description |
|---------|-------------|
| `sandclaude init` | Initialize project (creates `./.sandclaude/` and encrypts allowlist) |
| `sandclaude start` | Start Claude Code |
| `sandclaude start --disable-firewall` | Start without firewall (unrestricted network) |
| `sandclaude start --passthrough-firewall-and-write` | Allow all traffic, log unknown domains to `.sandclaude/allowed-domains.txt` |
| `sandclaude list` | Show project configuration |
| `sandclaude remove` | Remove `./.sandclaude/` (config, allowlist, key, logs) |
| `sandclaude firewall-reload` | Re-encrypt allowlist and SIGHUP running proxy |
| `sandclaude firewall-monitor` | Tail the allowlist proxy log |
| `sandclaude shell` | Open debug shell in container |
| `sandclaude rebuild` | Force rebuild Docker image (from `~/.sandclaude/assets`) |
| `sandclaude rebuild --destroy` | Rebuild from scratch (removes existing image/container first) |
| `sandclaude populate-proxy-credentials [--project]` | Populate global (or per-project) credentials |
| `sandclaude dashboard` | Start (or print the URL of) the host-wide project dashboard |
| `sandclaude dashboard stop` | Stop the dashboard server |
| `sandclaude help` | Show help |

## Usage

### Initialize Project

```bash
sandclaude init
```

Creates `./.sandclaude/` in the current directory. Prompts for:
- **Credential proxy?** (optional, strongly recommended): Enable mitmproxy to hide real credentials from Claude
- **Workspace directory**: Defaults to the current directory (resolved absolute path)

Config is stored in `./.sandclaude/project/config.json`, the allowlist in
`./.sandclaude/allowed-domains.txt`, and the encryption key in
`./.sandclaude/project/.allowlist-key`. `init` adds `.sandclaude/` to the project's
`.gitignore` automatically.

### Start Working

```bash
sandclaude start
```

Starts:
- Credential proxy (automatically if configured during init)
- Docker container with firewall
- Claude Code in dangerous mode

### Credential Proxy

When enabled during `init`, `sandclaude` runs `mitmweb` on the host and routes all container traffic through it. This prevents Claude from seeing or exfiltrating real credentials.

**How it works:**
1. `sandclaude start` launches `mitmweb` on the host
2. Container is started with `HTTP_PROXY`/`HTTPS_PROXY` pointing to `host.docker.internal:<port>`
3. Claude receives a dummy OAuth token inside the container
4. `mitmweb` intercepts outbound requests and injects the real credentials from the resolved credentials file
5. Proxy stops automatically when the container exits

**Credential resolution (global + per-project):**

Credentials are shared across projects by default and can be overridden per-project:

- **Global** — `~/.sandclaude/proxy-credentials.json` (written by `sandclaude populate-proxy-credentials`)
- **Per-project override** — `./.sandclaude/project/proxy-credentials.json` (written by `sandclaude populate-proxy-credentials --project`)

When both exist, they are merged **per-domain, project wins**: the project file
overrides or adds entries, and the global file fills in the rest. The merged result
is written to a temporary file and passed to `mitmweb`. An explicit
`SANDCLAUDE_PROXY_CREDS=/path` env var overrides all of this.

**Setup:**

```bash
sandclaude populate-proxy-credentials             # global (recommended default)
sandclaude populate-proxy-credentials --project   # per-project override
```

This interactively populates the credentials file using `claude setup-token` and `gh auth token`. You can also configure it manually:

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
sandclaude rebuild
```

**Proxy logs** are written to `.sandclaude/logs/mitm.log` in the directory where you run `sandclaude start`. View the mitmweb UI at `http://127.0.0.1:8081` while the session is running.

## Dashboard

`sandclaude dashboard` starts one long-lived, host-wide web page listing every project you've ever started, with tabs per project for a live terminal, the mitmweb credential-proxy UI, and the firewall allowlist log:

```bash
sandclaude dashboard        # start it (or print the URL if already running)
sandclaude dashboard stop   # stop it
```

`sandclaude start`/`dev` also start the dashboard daemon automatically (if it isn't already running) and open your default browser straight to that project's tab, so you don't need to run `sandclaude dashboard` yourself in the common case.

**Loopback-only, token-gated.** The dashboard binds to `127.0.0.1` only — never reachable from another machine — matching mitmweb's existing posture. On top of that, every route requires a random per-launch token, passed once as `?token=...` in the printed URL and then remembered as an `HttpOnly` cookie so reloading or reopening the page doesn't need it re-pasted. Loopback-only alone isn't enough here: the terminal tab grants a real shell, and a malicious page open in another browser tab could otherwise target `127.0.0.1` directly (a DNS-rebinding-style attack) — the token defends against that too. Treat the printed URL/token like a credential, not a bookmark.

**Reopening the page resumes exactly where you left off**, with no special app-level "session" logic — the dashboard is a thin, stateless viewer over backends that are already persistent on their own: the terminal tab reattaches to the same tmux dev session (full scrollback intact), the mitm tab reverse-proxies to the same long-running mitmweb process (its flow list is unaffected), and the firewall tab tails the log file directly off disk.

**⚠️ DinD-enabled projects run `--privileged` containers.** A shell reached through the dashboard's terminal tab for such a project is a near-direct path to host root — this is exactly why the token requirement isn't optional.

The terminal tab requires `tmux` (see Prerequisites) and only works for projects started with `sandclaude dev` (same requirement as `capture`/`send`/`attach` today) — plain `sandclaude start` sessions aren't backed by a tmux session to attach to. (The terminal itself is served by a built-in PTY-over-WebSocket bridge; no external `ttyd` is needed.)

## Firewall Management

The firewall is implemented as a Go HTTP CONNECT proxy (`allowlist-proxy`) running inside the container on `127.0.0.1:3128`. All of Claude's traffic is routed through it via `HTTP_PROXY`/`HTTPS_PROXY`. Connections to domains not in the allowlist are rejected.

### Allowed Domains File

The allowlist lives at `.sandclaude/allowed-domains.txt` (plaintext). When you modify it, run `firewall-reload` to re-encrypt and reload:

```bash
echo 'example.com' >> .sandclaude/allowed-domains.txt
sandclaude firewall-reload
```

This command:
1. Reads the encryption key from `.sandclaude/project/.allowlist-key`
2. Re-encrypts `allowed-domains.txt` → `allowed-domains.txt.enc`
3. If a container is running, sends SIGHUP to reload the allowlist without restart

**Encryption Key**: The encryption key is auto-generated during `init` and stored in `.sandclaude/project/.allowlist-key` (never committed). It is automatically read from this file by `sandclaude start` and `firewall-reload` - you don't need to set anything manually.

### Monitor Proxy Log

```bash
sandclaude firewall-monitor
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
sandclaude init
# ...
# Enable Docker-in-Docker (Claude can start inner containers)? [y/N]: y
# Port mappings to expose to host (e.g. 3000:3000,8000:8000, blank for none): 3000:3000
```

Port mappings can be changed later with `sandclaude update` (it re-prompts for DinD
ports), or by editing `.sandclaude/project/config.json` directly.

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
sandclaude start --dind-storage-driver=vfs
# Or set env var before starting:
DIND_STORAGE_DRIVER=vfs sandclaude start
```

**Check inner dockerd logs:**
```bash
sandclaude shell
cat .firewall/dockerd.log
```

**Inner container can't reach a domain:**
```bash
# Add domain to allowlist and reload proxy:
echo 'rubygems.org' >> .sandclaude/allowed-domains.txt
sandclaude firewall-reload
```

**Skip DinD for a single run:**
```bash
sandclaude start --disable-dind
```

## How It Works

1. Builds Docker image (matches host UID/GID)
2. Mounts workspace and credentials
3. Initializes iptables firewall with domain allowlist
4. Runs Claude with `--dangerously-skip-permissions`
5. Logs blocked connections for approval

**Firewall**: A Go HTTP CONNECT proxy (`allowlist-proxy`) runs inside the container on `127.0.0.1:3128`. Claude's `HTTP_PROXY`/`HTTPS_PROXY` env vars point to it. Connections to domains not in the allowlist are rejected with a `403`. The allowlist is stored encrypted at `.sandclaude/allowed-domains.txt.enc` (bind-mounted into the container) and supports hot-reload via `sandclaude firewall-reload`. Logs go to `logs/proxy.log` inside the container.

**Skill System**: the skills in `sandbox/skills/` are baked into the image (a workspace's own `.claude/skills/` is layered on top at runtime). `sandbox/skills/` is a duplicate of this repo's own `.claude/skills/` — the root copy makes them active when developing sandclaude itself; keep the two in sync. `environment/SKILL.md` teaches Claude:
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
sandclaude firewall-monitor

# Add a domain and hot-reload (no container restart needed)
echo 'example.com' >> .sandclaude/allowed-domains.txt
sandclaude firewall-reload
```

**Proxy not starting:**
```bash
# Check proxy log inside container
sandclaude shell
cat logs/proxy.log
```

**Disable firewall for debugging:**
```bash
sandclaude start --disable-firewall
```

## Developing sandclaude

The local development loop for sandclaude itself does **not** require installing.
When you run `./sandclaude` directly from the git checkout, the binary detects that
the `sandbox/` (Docker build context) and `host/` (host-loaded assets) tier dirs sit
right beside it and uses the checkout as the asset root. So the iterate loop is:

```bash
# edit internal/**/*.go / sandbox/Dockerfile / sandbox/launcher.py …
go build -o sandclaude ./cmd/sandclaude
./sandclaude list          # runs against the checkout's assets, no install needed
```

The Go code is organized as `cmd/sandclaude` (entrypoint) + `internal/` packages
(config, creds, session, proxy, dashboard, container, cli). Non-Go assets live by
runtime tier: `sandbox/` (everything built into or mounted into the sandbox image)
and `host/` (assets loaded by host processes, e.g. `proxy-addon.py` for the host
mitmweb). See `docs/architecture.md`.

Asset resolution order (see `AssetsDir()` / `HostAssetsDir()` in `internal/config/paths.go`):
1. `$SANDCLAUDE_HOME/assets/{sandbox,host}` — explicit override / installed layout
2. `<binary dir>/assets/{sandbox,host}` — installed next to the binary
3. `<binary dir>/{sandbox,host}` — **dev mode**: the git checkout beside `./sandclaude`

When you're ready to "ship" your changes to the globally-installed CLI, re-run
`./install.sh` — it rebuilds the binary and re-syncs `~/.sandclaude/assets`.

## Security

- Container runs as non-root (matches host UID/GID)
- Ephemeral container (`--rm` flag)
- Workspace mounted read-write, credentials read-only
- Network firewall blocks unapproved domains
- Credential proxy prevents Claude from seeing real credentials

## License

Public domain / use freely
