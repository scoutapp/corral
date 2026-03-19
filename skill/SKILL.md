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
- **Tool**: iptables + ipset
- **Config**: `/home/claude/.firewall/allowed-domains.txt`
- **Logging**: Blocked connections logged with `FIREWALL_BLOCKED` prefix
- **Interactive mode**: Enabled by default at `/home/claude/.firewall/interactive-mode`

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

When Claude Code needs to access a blocked domain, you can approve it:

**Method 1: Interactive monitoring**
```bash
firewall-helper.sh monitor
```
This watches kernel logs and prompts you to approve blocked connections in real-time.

**Method 2: Manual addition**
```bash
firewall-helper.sh add example.com
```

**Method 3: Edit config file**
```bash
vim /home/claude/.firewall/allowed-domains.txt
sudo /usr/local/bin/init-firewall.sh
```

### Listing Allowed Domains
```bash
firewall-helper.sh list
```

### Removing Domains
```bash
firewall-helper.sh remove example.com
```

### Clearing Cache
The helper maintains a cache of already-prompted blocked IPs. To reset:
```bash
firewall-helper.sh clear-cache
```

### Disabling Interactive Mode
```bash
echo 'disabled' > /home/claude/.firewall/interactive-mode
```

## Firewall Technical Details

**Initialization:** `sudo /usr/local/bin/init-firewall.sh`
- Runs on container start via devcontainer postStartCommand
- Preserves Docker DNS rules (127.0.0.11)
- Resolves all configured domains to IPs
- Creates ipset with all allowed IPs and CIDR ranges
- Sets iptables rules: default DROP, allow only ipset members

**Architecture:**
1. Domain list → DNS resolution → IP addresses
2. IP addresses → ipset (hash:net) → allows CIDR ranges
3. iptables OUTPUT chain → match ipset → ACCEPT or REJECT
4. Rejected packets → logged with `FIREWALL_BLOCKED` prefix

**Performance:**
- ipset lookups are O(1)
- No per-packet DNS resolution
- Re-initialization required when adding domains (handles DNS changes)

**Limitations:**
- IPs resolved at firewall init time (changes require reload)
- Domain names not stored in iptables (only resolved IPs)
- Wildcard domains not supported (add specific subdomains)

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

### Approving a new domain on-the-fly
```bash
# Terminal 1: Run Claude
dangerous-claude

# Terminal 2: Monitor firewall (inside container via docker exec)
firewall-helper.sh monitor

# When blocked connection appears, approve or deny
# Firewall reloads automatically, Claude can retry
```

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
# Check firewall status
sudo iptables -L -v -n

# Check ipset contents
sudo ipset list allowed-domains

# View recent blocked connections
sudo dmesg | grep FIREWALL_BLOCKED | tail -20

# Manually reload firewall
sudo /usr/local/bin/init-firewall.sh
```

## Integration Patterns

### As a git submodule
```bash
cd ~/my-project
git submodule add https://github.com/yourname/dangerous-claude .dangerous-claude
.dangerous-claude/dangerous-claude
```

### As a devcontainer base
Copy `.devcontainer/` and firewall scripts to your project:
```bash
cp -r .devcontainer ~/my-project/
cp init-firewall.sh ~/my-project/
cp firewall-helper.sh ~/my-project/
# Edit Dockerfile to add project-specific tools
# Edit init-firewall.sh to add project-specific domains
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

## Firewall Troubleshooting

**Symptom:** `curl: (7) Failed to connect`
- **Cause:** Domain not in allowlist
- **Fix:** `firewall-helper.sh add example.com` or run `firewall-helper.sh monitor`

**Symptom:** DNS resolution fails
- **Cause:** Firewall blocks DNS or Docker DNS broken
- **Fix:** Check DNS rules: `sudo iptables -L OUTPUT -n | grep 53`

**Symptom:** GitHub access works but npm install fails
- **Cause:** npm registry IPs changed since last firewall init
- **Fix:** `sudo /usr/local/bin/init-firewall.sh` (re-resolves all domains)

**Symptom:** Firewall init fails with "permission denied"
- **Cause:** Container missing `NET_ADMIN` capability
- **Fix:** Ensure `--cap-add=NET_ADMIN --cap-add=NET_RAW` in docker run args

**Symptom:** All connections blocked including allowed domains
- **Cause:** ipset not loaded or iptables rules not applied
- **Fix:** Check `sudo ipset list allowed-domains` and `sudo iptables -L OUTPUT -v`

## Architecture Decisions

**Why iptables instead of application-level proxies?**
- No need to configure each tool (npm, curl, gh, etc.)
- Transparent to all applications
- Lower overhead
- Harder to bypass accidentally

**Why ipset instead of individual iptables rules?**
- O(1) lookups vs O(n) rule traversal
- Supports CIDR ranges efficiently
- Single ipset can hold thousands of IPs

**Why DNS resolution at init instead of runtime?**
- No DNS lookup latency per connection
- Simpler iptables rules (IP-based)
- Tradeoff: Must reload firewall when IPs change

**Why interactive mode by default?**
- Developer-friendly: see what's blocked, approve easily
- Opt-out design: Can disable for production-like testing
- Logging has minimal overhead

## Extending the Firewall

**Adding bulk domains:**
```bash
cat >> /home/claude/.firewall/allowed-domains.txt <<EOF
api.stripe.com
api.twilio.com
slack.com
EOF
sudo /usr/local/bin/init-firewall.sh
```

**Adding IP ranges directly:**
Edit `init-firewall.sh` and add after domain resolution:
```bash
ipset add allowed-domains 192.168.1.0/24
ipset add allowed-domains 10.0.0.0/8
```

**Allowing all traffic (disable firewall):**
```bash
sudo iptables -P OUTPUT ACCEPT
sudo iptables -F OUTPUT
```

**Re-enabling firewall:**
```bash
sudo /usr/local/bin/init-firewall.sh
```

## Known Limitations

1. **No wildcard domain support** - Must add each subdomain explicitly
2. **DNS changes require reload** - IPs cached at init time
3. **No per-process rules** - Firewall applies to entire container
4. **Logging overhead** - High connection rate can flood kernel log
5. **Requires privileged capabilities** - NET_ADMIN and NET_RAW needed

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
