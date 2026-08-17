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
- Real credentials never enter the container — injected by the host proxy

## Environment Details

**Container capabilities:**
- `NET_ADMIN` - Required for iptables firewall management
- `NET_RAW` - Required for network packet filtering

**Firewall implementation:**
- **Tool**: Go allowlist-proxy (HTTP CONNECT proxy) + iptables egress enforcement
- **Config**: `/home/claude/allowed-domains.txt.enc` (AES-256-GCM encrypted, bind-mounted from host)
- **Logging**: `/home/claude/logs/proxy.log` (ALLOWED/BLOCKED entries per request)
- **Hot-reload**: Send SIGHUP to allowlist-proxy or run `corral firewall-reload` from host

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
./corral firewall-reload [project]
```

**Method 2: Restart the container**
```bash
# Edit the plaintext allowlist
vim allowlist-proxy/allowed-domains.txt
# Re-encrypt
./corral firewall-reload
# Restart
./corral start [project]
```

### Viewing the Proxy Log
```bash
# From host
./corral firewall-monitor [project]
# Inside container
tail -f ~/logs/proxy.log
```

## Firewall Technical Details

**Initialization:** Runs automatically in `entrypoint.sh` on container start.

**Architecture:**
1. Encrypted `allowed-domains.txt.enc` (bind-mounted from host) → decrypted in-memory by allowlist-proxy
2. allowlist-proxy runs two listeners: an **explicit** HTTP CONNECT proxy on `0.0.0.0:3128` (outer processes reach it via `HTTP_PROXY`) and a **transparent** listener on `0.0.0.0:3129` (for iptables-REDIRECTed DinD inner-container traffic). Both validate each request by domain (CONNECT/Host for explicit, `SO_ORIGINAL_DST` + SNI for transparent).
3. iptables OUTPUT chain → allow only `proxyuser` (the proxy's UID) for direct TCP; all others REJECT. Inner containers are forced through the transparent listener by a `nat PREROUTING REDIRECT` to `:3129`.
4. All traffic must flow through the proxy; ALLOWED/BLOCKED logged to `~/logs/proxy.log` (transparent entries are tagged `(transparent)`)

**Hot-reload:**
- Write new `.enc` file to host (via `corral firewall-reload`)
- Bind mount means container sees it immediately
- SIGHUP to allowlist-proxy atomically swaps the in-memory domain set

**Performance:**
- O(1) domain lookups (hash map)
- No DNS resolution — domain-level matching with subdomain support
- No restart required to update allowlist

## Credential Handling

**Rule: Credentials NEVER enter the container filesystem in readable form.**

Real credentials are held on the HOST and injected by the credential proxy — they
never enter the container. Inside the container you only ever have dummy tokens:
- Claude's `CLAUDE_CODE_OAUTH_TOKEN` is a dummy; the proxy injects the real one.
- `GH_TOKEN` is a dummy so `gh`/`git` don't prompt for login; the proxy injects the
  real GitHub token into `api.github.com` requests. `gh auth token` and
  `gh auth status --show-token` are disabled by an in-container `gh` wrapper.

There is no readable GitHub or Claude credential file in the container. Running
`gh auth token` or `echo $GH_TOKEN` will not reveal a real secret — don't try.

**Never ask Claude to:**
- Read credential files or echo/display tokens
- Copy credentials elsewhere, commit them, or send them over the network

If Claude needs authenticated access:
- GitHub: just use `gh` / `git` normally — the proxy injects the real token into
  `api.github.com` requests. You never need (and cannot read) the raw token.
- Other services: their credentials are injected by the proxy too when configured
  via `corral set-cred` / `populate-proxy-credentials`.

## Docker-in-Docker (DinD)

When `DIND_ENABLED=1` is set in the environment, an inner Docker daemon is running at `unix:///var/run/dind/docker.sock`. `DOCKER_HOST` is already exported to point there.

**Key facts:**
- All `docker` commands you run go to the **inner daemon**, not the host
- Inner containers sit on `172.18.0.0/16` bridge network
- **Inner-container egress is captured transparently** — no `HTTP_PROXY` env var is injected into containers and none is needed. An iptables `nat PREROUTING REDIRECT` rule sends all inner-container TCP egress (`172.16.0.0/12`, external destinations) to the allowlist proxy's **transparent listener on `:3129`**, which recovers the original destination (`SO_ORIGINAL_DST`) and the SNI/Host, enforces the allowlist, and tunnels to mitmproxy. Containers need zero proxy configuration.
- Inner containers do their **own DNS resolution**. A `nat POSTROUTING MASQUERADE` rule (for `172.18.0.0/16 → non-inner`) SNATs their UDP/53 queries so DNS works without proxy env vars.
- The allowlist proxy runs two listeners: the **explicit** HTTP CONNECT proxy on `0.0.0.0:3128` (used by the outer container's own processes via `HTTP_PROXY`, and as the `--upstream` chain target) and the **transparent** listener on `0.0.0.0:3129` (for REDIRECTed inner-container traffic).
- The proxy forwards to mitmproxy, so inner containers must trust the mitmproxy CA cert for TLS interception. CA trust is handled at two points:
  - **Build time** — the `~/bin/docker` wrapper (shadows `/usr/bin/docker`) intercepts `docker build` / `docker buildx build` and rewrites the Dockerfile **in memory** (the original on disk is never touched/committed): after every `FROM` it installs the mitm CA into the OS trust store (Debian/Alpine/RHEL-aware) and sets every toolchain's CA-override env (`NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`/`PIP_CERT`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `CARGO_HTTP_CAINFO`). This is why `RUN npm install` / `pip install` / `bundle` / `go mod` / `mix deps.get` work through the MITM proxy with no Dockerfile changes. The CA is passed in via a `--build-context` named `corral-ca`. (This is needed because BuildKit has no supported way to inject a CA into build RUN steps, and Node/Python/Rust ship their own CA bundles that ignore the OS store.)
  - **Run time** — the **cert-injector** sidecar watches docker events and injects the CA into new inner containers. **Caveat:** its create→start→restart model is racy for very short-lived `--rm` one-shot containers (they may exit before injection completes). Long-running service containers, and any container built via the wrapper (the CA is already baked into the image), are fine. For an ephemeral container that hits `SSL certificate problem` / `509 Certificate Verify Failed`, either mount the CA read-only (`-v /etc/proxy-ca.crt:/tmp/ca.crt:ro` and point the tool at it) or use the tool's insecure flag.
- iptables allows `172.16.0.0/12` in the OUTPUT chain so the proxy can respond to inner container connections; a matching `nat POSTROUTING MASQUERADE` (also `172.16.0.0/12`) lets inner containers' own DNS reach the resolver. **Both must cover the full `/12`, not just the default bridge `172.18.0.0/16`** — docker-compose apps get networks like `172.19.x`/`172.20.x`, and scoping to `/16` silently breaks their DNS.
- Inner containers are destroyed when corral exits, but their **named volumes and pulled images persist**

**Note on the outer container's own processes (Claude, curl, etc.):** these are NOT captured transparently — they use an explicit `HTTP_PROXY=127.0.0.1:3128` env var (set once in `entrypoint.sh`), enforced by the OUTPUT-chain REJECT. Transparent capture of locally-generated traffic does not work reliably inside Docker Desktop's LinuxKit VM, so only DinD inner containers are env-free. The dockerd daemon's own image pulls go through the proxy via `HTTP_PROXY` in its process environment (not injected into the containers it starts).

## DinD Volume Persistence

The inner docker data root (`/var/lib/docker-dind`) is bind-mounted from `./project/dind-data/` on the host. This means:

- **Named volumes** (e.g. postgres data, redis data) survive outer container restarts — the data is on your Mac, not inside the ephemeral container
- **Pulled images** are cached — `docker pull` only runs on first use; subsequent starts reuse the cached layers
- **Inner containers themselves** are destroyed on exit and must be restarted by Claude — but their volumes are intact, so databases come back with existing data

**Which data belongs to which project?** The data dir is keyed to `./project/dind-data/` in the corral repo directory. Each project has exactly one data dir — deterministic and isolated from other projects.

**To restart services after an outer container restart:**
```bash
# Volumes are intact; just re-run the containers that use them
docker run -d --name postgres -v pgdata:/var/lib/postgresql/data postgres:16
docker run -d --name redis -v redisdata:/data redis:7
```

**To reset inner docker state** (wipe all inner images and volumes):
```bash
# From host, outside the container
rm -rf ./project/dind-data/
```

**To inspect persisted volumes from the host:**
```bash
ls ./project/dind-data/volumes/
```

**Running inner containers:**
```bash
# Build and start a Rails app
docker build -t myapp .
docker run -d -p 3000:3000 --name rails myapp

# Shell into it
docker exec -it rails bash

# Logs
docker logs rails -f

# Port 3000 is accessible on the user's host machine at http://localhost:3000
# (because the outer corral container has -p 3000:3000)
```

**Making a service viewable in the dashboard Live View:**
The dashboard has a **Live View** tab that embeds a web app you're running so the
user can watch it live. For an inner (DinD) service to be viewable — and reachable
by the dashboard at all — run its container with a **published port**:

```bash
docker run -d -p 3000:3000 --name web myapp
```

Publishing with `-p` exposes the service on the DinD bridge, which is what the
dashboard reverse-proxies to. A container that only `EXPOSE`s a port, or binds a
port privately inside its own network, will **not** appear in Live View and can't
be reached from the host — always `-p <port>:<port>` the thing you want the user
to see. (An app you run directly in this outer container, bound to `0.0.0.0` or
`127.0.0.1`, is viewable without `-p`.)

**Inner container network access:**
- Inner containers go through the same allowlist proxy as everything else (transparently — see DinD section above)
- If an inner container needs a domain not in the allowlist, add it to the plaintext allowlist **on the host** and reload:
  ```bash
  # On the host, in the corral repo:
  echo 'rubygems.org' >> allowlist-proxy/allowed-domains.txt
  ./corral firewall-reload   # re-encrypts and SIGHUPs the proxy
  ```
  (From inside the container you can append to `/home/claude/allowed-domains.txt`, which is bind-mounted to that same host file; the user still runs `corral firewall-reload` to re-encrypt and reload.)
- Common domains to add for Rails: `rubygems.org`, `index.rubygems.org`
- Common domains to add for Django/Python: `pypi.org`, `files.pythonhosted.org`

**Multi-container apps:**
```bash
# Containers can talk to each other by name when on the same docker network
docker network create app-net
docker run -d --network app-net --name postgres postgres:16
docker run -d --network app-net --name redis redis:7
docker run -d --network app-net -p 3000:3000 --name web myapp
```

**Verify inner daemon:**
```bash
docker info          # Shows inner daemon info
docker ps            # Lists only inner containers
echo $DOCKER_HOST    # unix:///var/run/dind/docker.sock
```

**Storage driver issues:**
If image builds fail with overlay2 errors, the `DIND_STORAGE_DRIVER=vfs` fallback works universally (slower but compatible). Check `cat .firewall/dockerd.log` for errors.

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
./corral start

# Claude can now:
# - Install npm/yarn packages (allowlist includes registries)
# - Clone from GitHub (allowlist includes GitHub)
# - Run builds and tests
# - Access Claude API
```

### Adding a domain on-the-fly (hot-reload)
```bash
# Terminal 1: Run Claude
./corral start

# Terminal 2: When a connection is blocked, add the domain and reload
echo "api.example.com" >> allowlist-proxy/allowed-domains.txt
./corral firewall-reload

# Claude can now retry — no container restart needed
```

### Passthrough mode (auto-discover domains)
Use `--passthrough-firewall-and-write` to allow all outbound traffic while automatically logging unknown domains to `allowlist-proxy/allowed-domains.txt` on the host. Useful for discovering what domains a workflow needs before locking it down.

```bash
# Start in passthrough mode
corral start --passthrough-firewall-and-write

# Inside the container, unknown domains are allowed and appended to:
#   /home/claude/allowed-domains.txt  (bind-mounted from host at allowlist-proxy/allowed-domains.txt)

# After your session, the host file allowlist-proxy/allowed-domains.txt
# will contain all domains that were accessed. Review and reload:
./corral firewall-reload
```

**Note:** The passthrough log is mounted at `/home/claude/allowed-domains.txt` inside the container. This is the same file as `allowlist-proxy/allowed-domains.txt` on the host — changes are reflected immediately on both sides.

**Important:** If you are unsure whether the firewall is active, **always attempt to reach the URL anyway** — do not assume it is blocked. Check `ps aux | grep allowlist-proxy` to confirm whether the proxy is running.

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

### Integrating into an existing project
Copy files to your project using the built-in command:
```bash
./corral copy ~/my-project
# Edit Dockerfile to add project-specific tools
# Edit allowlist-proxy/allowed-domains.txt to add project-specific domains
# Run: ./corral firewall-reload
```

**Important:** If Claude attempts to access a site blocked by the proxy, Claude should:
1. Add the domain to `/home/claude/allowed-domains.txt` — this file is always bind-mounted from `allowlist-proxy/allowed-domains.txt` on the host (read-write), so edits are immediately visible to the host
2. Notify the user to run `corral firewall-reload` from the host to encrypt the updated file and SIGHUP the proxy
3. Retry the request once the user confirms the reload is done

In passthrough mode (`--passthrough-firewall-and-write`), unknown domains are appended to `/home/claude/allowed-domains.txt` automatically — no manual editing needed.

## Firewall Troubleshooting

**Symptom:** `curl: (7) Failed to connect` or HTTP 403 from proxy
- **Cause:** Domain not in allowlist
- **Fix:** Restart with `corral start --passthrough-firewall-and-write` to auto-log needed domains, then `corral firewall-reload` to lock it back down

**Symptom:** DNS resolution fails
- **Cause:** Firewall blocks DNS or Docker DNS broken
- **Fix:** Check DNS rules: `sudo iptables -L OUTPUT -n | grep 53`

**Symptom:** Proxy fails to start — "decrypt allowlist" error
- **Cause:** Wrong `ALLOWLIST_KEY` or corrupted `.enc` file
- **Fix:** Re-run `corral firewall-reload` with the correct `ALLOWLIST_KEY`

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
./corral firewall-reload
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
