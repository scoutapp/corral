---
name: dangerous-claude-firewall
description: Sandboxed Claude Code environment with network firewall protection. Run Claude in dangerous mode while restricting outbound connections to approved domains.
---

> **CRITICAL:** This skill enables Claude Code to run with `--dangerously-skip-permissions` inside a Docker container with network firewall protection. The firewall restricts all outbound connections to pre-approved domains. Interactive approval system allows adding domains on-demand.

# Dangerous Claude with Firewall

## Overview

This is a sandboxed environment for running Claude Code in "dangerous mode" (no permission prompts) with network-level security controls. All outbound connections are restricted by iptables firewall rules to a configurable allowlist.

**Security model:**
- Claude can execute commands without asking (dangerous mode)
- Network firewall restricts what external services Claude can reach
- Interactive approval system for adding new domains
- Credentials mounted read-only, never exposed in container

## Environment Details

**Container capabilities:**
- `NET_ADMIN` - Required for iptables firewall management
- `NET_RAW` - Required for network packet filtering

**Firewall implementation:**
- **Tool**: Go allowlist-proxy (HTTP CONNECT proxy) + iptables egress enforcement
- **Config**: `/home/claude/allowed-domains.txt.enc` (AES-256-GCM encrypted, bind-mounted from host)
- **Logging**: `/home/claude/logs/proxy.log` (ALLOWED/BLOCKED entries per request)
- **Hot-reload**: Send SIGHUP to allowlist-proxy or run `sandclaude reload-firewall` from host

## Pre-approved Domains

The firewall comes with these domains pre-configured:

**Claude Code & Development:**
- `api.anthropic.com` - Claude API
- `registry.npmjs.org` - npm packages
- `registry.yarnpkg.com` - Yarn packages
- `npm.pkg.github.com` - GitHub npm registry
- GitHub API/web/git (all IP ranges from api.github.com/meta)
- `marketplace.visualstudio.com` - VS Code extensions
- `vscode.blob.core.windows.net` - VS Code assets
- `update.code.visualstudio.com` - VS Code updates
- `statsig.anthropic.com`, `statsig.com` - Feature flags
- `sentry.io` - Error tracking

**Go Development:**
- `proxy.golang.org` - Go module proxy
- `sum.golang.org` - Go checksum database
- `go.googlesource.com` - Go source repos
- `golang.org` - Go official site

**CDNs:**
- `cdn.jsdelivr.net`
- `unpkg.com`

**Container Registries:**
- `registry.hub.docker.com`
- `ghcr.io`
- `quay.io`
- `pkgs.dev.azure.com`

**Always allowed (not in config file):**
- DNS (port 53)
- SSH (port 22)
- Localhost
- Host Docker network

## Firewall Management

### Adding Domains

When Claude Code needs to access a blocked domain, edit the allowlist and reload:

**Method 1: Hot-reload from host (container stays running)**
```bash
# Edit the plaintext allowlist
vim allowlist-proxy/allowed-domains.txt
# Encrypt and SIGHUP the proxy
./sandclaude reload-firewall [project]
```

**Method 2: Restart the container**
```bash
# Edit the plaintext allowlist
vim allowlist-proxy/allowed-domains.txt
# Re-encrypt
./sandclaude reload-firewall
# Restart
./sandclaude start [project]
```

### Viewing the Proxy Log
```bash
# From host
./sandclaude firewall-monitor [project]
# Inside container
tail -f ~/logs/proxy.log
```

## Firewall Technical Details

**Initialization:** Runs automatically in `entrypoint.sh` on container start.

**Architecture:**
1. Encrypted `allowed-domains.txt.enc` (bind-mounted from host) → decrypted in-memory by allowlist-proxy
2. allowlist-proxy listens on `127.0.0.1:3128` — validates each CONNECT/HTTP request by domain
3. iptables OUTPUT chain → allow only `proxyuser` (the proxy's UID) for direct TCP; all others REJECT
4. All traffic must flow through the proxy; ALLOWED/BLOCKED logged to `~/logs/proxy.log`

**Hot-reload:**
- Write new `.enc` file to host (via `sandclaude reload-firewall`)
- Bind mount means container sees it immediately
- SIGHUP to allowlist-proxy atomically swaps the in-memory domain set

**Performance:**
- O(1) domain lookups (hash map)
- No DNS resolution — domain-level matching with subdomain support
- No restart required to update allowlist

## Credential Handling

**Rule: Credentials NEVER enter the container filesystem in readable form.**

The `dangerous-claude` wrapper mounts:
- `~/.claude` → `/home/claude/.claude` (Claude credentials)
- `~/.claude.json` → `/home/claude/.claude.json` (session state)
- `~/.config/gh` → `/home/claude/.config/gh:ro` (GitHub CLI, read-only)
- `$GH_TOKEN` environment variable (if `gh auth token` succeeds)

All credential mounts are handled by the Docker runtime. They exist in the container but are never copied, logged, or written elsewhere.

**Never ask Claude to:**
- Read credential files directly
- Echo or display tokens
- Copy credentials to other locations
- Commit credentials to git
- Send credentials over network

If Claude needs authenticated access:
- GitHub: Use `gh` CLI (already authenticated via mount)
- npm: Use `npm login` interactively (persists to mounted `~/.claude` if needed)
- Other services: Mount credentials read-only or use env vars

## When to Use This Skill

**Use this environment when:**
- Running untrusted or experimental code
- Allowing Claude full command execution without prompts
- Testing integrations that need network access
- Building/testing Node.js or Go projects
- Need both freedom and network-level safety

**Do NOT use when:**
- You need full unrestricted internet (defeats the purpose)
- Working with services not in the allowlist (add them first)
- Production deployments (this is a development sandbox)

## Common Workflows

### Starting a new project
```bash
# From host
dangerous-claude ~/projects/my-new-app

# Claude can now:
# - Install npm/yarn packages (allowlist includes registries)
# - Clone from GitHub (allowlist includes GitHub)
# - Run builds and tests
# - Access Claude API
```

### Adding a domain on-the-fly (hot-reload)
```bash
# Terminal 1: Run Claude
sandclaude start myproject

# Terminal 2: When a connection is blocked, add the domain and reload
echo "api.example.com" >> allowlist-proxy/allowed-domains.txt
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude reload-firewall myproject

# Claude can now retry — no container restart needed
```

### Passthrough mode (auto-discover domains)
Use `--disable-firewall-and-write` to allow all outbound traffic while automatically logging unknown domains to `allowlist-proxy/allowed-domains.txt` on the host. Useful for discovering what domains a workflow needs before locking it down.

```bash
# Start in passthrough mode
sandclaude start --disable-firewall-and-write

# Inside the container, unknown domains are allowed and appended to:
#   /home/claude/allowed-domains.txt  (bind-mounted from host at allowlist-proxy/allowed-domains.txt)

# After your session, the host file allowlist-proxy/allowed-domains.txt
# will contain all domains that were accessed. Review and encrypt:
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude reload-firewall
```

**Note:** The passthrough log is mounted at `/home/claude/allowed-domains.txt` inside the container. This is the same file as `allowlist-proxy/allowed-domains.txt` on the host — changes are reflected immediately on both sides.

### Using with VS Code devcontainer
```bash
# Open project in VS Code
code ~/projects/my-app

# VS Code detects .devcontainer/devcontainer.json
# Rebuilds container with firewall (requires --cap-add flags)
# Opens remote session with firewall active
```

### Debugging firewall issues
```bash
# View proxy log (ALLOWED/BLOCKED entries)
tail -f ~/logs/proxy.log

# Check iptables rules
sudo iptables -L OUTPUT -v -n

# Check which process is the proxy
ps aux | grep allowlist-proxy

# Manual SIGHUP to reload allowlist (inside container)
pkill -HUP -x allowlist-proxy
```

## Integration Patterns

### As a git submodule
```bash
cd ~/my-project
git submodule add https://github.com/yourname/dangerous-claude .dangerous-claude
.dangerous-claude/dangerous-claude
```

### As a devcontainer base
Copy files to your project using the built-in command:
```bash
./sandclaude copy ~/my-project
# Edit Dockerfile to add project-specific tools
# Edit allowlist-proxy/allowed-domains.txt to add project-specific domains
# Run: export ALLOWLIST_KEY=<passphrase> && ./sandclaude reload-firewall
```

### As a skill for other projects
This SKILL.md can be loaded by any Claude Code session via:
```bash
claude --skill /path/to/this/skill/SKILL.md
```

Claude will then know:
- How the firewall works
- What domains are allowed
- How to add domains
- Credential handling rules
- When to ask for domain approval

**Important:** If Claude attempts to access a site blocked by the proxy, Claude should:
1. Add the domain to `/home/claude/allowed-domains.txt` — this file is always bind-mounted from `allowlist-proxy/allowed-domains.txt` on the host (read-write), so edits are immediately visible to the host
2. Notify the user to run `sandclaude reload-firewall` from the host to encrypt the updated file and SIGHUP the proxy
3. Retry the request once the user confirms the reload is done

In passthrough mode (`--disable-firewall-and-write`), unknown domains are appended to `/home/claude/allowed-domains.txt` automatically — no manual editing needed.

## Firewall Troubleshooting

**Symptom:** `curl: (7) Failed to connect` or HTTP 403 from proxy
- **Cause:** Domain not in allowlist
- **Fix:** Restart with `sandclaude start --disable-firewall-and-write` to auto-log needed domains, then `sandclaude reload-firewall` to lock it back down

**Symptom:** DNS resolution fails
- **Cause:** Firewall blocks DNS or Docker DNS broken
- **Fix:** Check DNS rules: `sudo iptables -L OUTPUT -n | grep 53`

**Symptom:** Proxy fails to start — "decrypt allowlist" error
- **Cause:** Wrong `ALLOWLIST_KEY` or corrupted `.enc` file
- **Fix:** Re-run `sandclaude reload-firewall` with the correct `ALLOWLIST_KEY`

**Symptom:** Firewall not enforced (processes bypass proxy)
- **Cause:** Container missing `NET_ADMIN` capability
- **Fix:** Ensure `--cap-add=NET_ADMIN --cap-add=NET_RAW` in docker run args

**Symptom:** All connections blocked including allowed domains
- **Cause:** iptables rules not applied or proxy not running
- **Fix:** Check `sudo iptables -L OUTPUT -n` and `ps aux | grep allowlist-proxy`

## Architecture Decisions

**Why application-level proxy instead of iptables/ipset?**
- Domain-level matching (not IP-level) — handles CDNs and cloud services correctly
- Hot-reloadable allowlist without touching iptables
- Readable ALLOWED/BLOCKED log per request
- No DNS pre-resolution needed

**Why encrypt the allowlist?**
- Allowlist reflects permitted capabilities — shouldn't be trivially editable inside the container
- AES-256-GCM provides both confidentiality and integrity (tamper detection)
- Key stays on host; container only has the ciphertext + key via env var at runtime

**Why iptables on top of the proxy?**
- Prevents processes from bypassing the proxy via direct TCP (env var bypass, etc.)
- Only `proxyuser` (the proxy UID) can make direct outbound TCP connections

## Extending the Firewall

**Adding domains:**
```bash
# On host
echo "api.stripe.com" >> allowlist-proxy/allowed-domains.txt
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude reload-firewall [project]
```

**Allowing all traffic (disable firewall):**
```bash
sudo iptables -P OUTPUT ACCEPT
sudo iptables -F OUTPUT
```

## Known Limitations

1. **No wildcard domain support** - Must add each subdomain explicitly
2. **No per-process rules** - Firewall applies to entire container
3. **Requires privileged capabilities** - NET_ADMIN and NET_RAW needed
4. **Key in env var** - ALLOWLIST_KEY visible in process list inside container

## Security Considerations

**What the firewall protects against:**
- Accidental data exfiltration to unauthorized domains
- Supply chain attacks (malicious npm packages calling home)
- Unintended API calls during development
- Credential leakage via HTTP to unknown endpoints

**What the firewall does NOT protect against:**
- Attacks using allowed domains (e.g., GitHub gists, npm packages)
- Local privilege escalation (container runs as non-root but has sudo)
- Attacks via mounted volumes (workspace is read-write)
- DNS tunneling (DNS is unrestricted for resolution)

**Additional hardening (not implemented):**
- Drop `sudo` access entirely (requires manual firewall init)
- Read-only workspace mount (breaks most development workflows)
- Restrict DNS to specific nameservers
- Rate limit allowed domains
- Network namespace isolation

This is a **development sandbox**, not a production security boundary. Use for local experimentation, not for running untrusted production workloads.
