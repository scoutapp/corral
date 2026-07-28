# Sandclaude

Run Claude Code in any project — sandboxed, network-firewalled, with a live web
dashboard and full Docker-in-Docker.

Claude runs in **dangerous mode** (no permission prompts) inside an ephemeral
container whose outbound network is restricted to an allowlist, and whose real
credentials it never sees (a host-side proxy injects them). You get the autonomy
of `--dangerously-skip-permissions` with guardrails around it.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/scoutapp/sandclaude/main/scripts/install.sh | bash
```

Installs the `sandclaude` binary to your `$PATH` and the runtime asset bundle to
`~/.sandclaude/assets`. Needs `docker`; installs `mitmproxy` for you if missing.

| Env var | Purpose | Default |
|---|---|---|
| `SANDCLAUDE_VERSION` | pin a release | latest |
| `SANDCLAUDE_PREFIX` | binary dir | `/usr/local/bin`, else `~/.local/bin` |
| `SANDCLAUDE_HOME` | data dir | `~/.sandclaude` |

<sub>From source (dev): `git clone … && cd sandclaude && ./install.sh`. See [Developing](#developing).</sub>

## Use

```bash
cd ~/my-project
sandclaude init     # one-time: config, allowlist, encryption key (writes ./.sandclaude/)
sandclaude start    # launches Claude, opens the dashboard in your browser
```

`init` prompts for the credential proxy (**recommended**), Docker-in-Docker, and
any host port mappings. Then set credentials once, globally, reused everywhere:

```bash
sandclaude populate-proxy-credentials             # ~/.sandclaude/proxy-credentials.json
sandclaude populate-proxy-credentials --project   # per-project override (wins per-domain)
```

> You must authenticate Claude Code on the host first (`claude` to sign in) — this
> writes `~/.claude.json` (session state, not secrets), mounted into the container.

### New project? Discover required domains

Run in passthrough mode, work normally (installs, clones, builds) — every accessed
domain is logged to `allowed-domains.txt`. Then lock it down:

```bash
sandclaude start --passthrough-firewall-and-write   # allow all, record unknowns
sandclaude firewall-reload                          # encrypt the discovered allowlist
sandclaude start                                    # enforce it
```

## Commands

| Command | Description |
|---|---|
| `init` / `update` / `remove` | manage `./.sandclaude/` project config |
| `start` | launch Claude detached + open the dashboard (browser-first) |
| `start --foreground` | classic attached in-terminal session |
| `start --disable-firewall` / `--disable-dind` / `--passthrough-firewall-and-write` | start variants |
| `dev` | start detached in a tmux session for closed-loop dev (`capture`/`send`/`attach`) |
| `list` | show project config |
| `firewall-reload` / `firewall-monitor` | re-encrypt+reload allowlist / tail the proxy log |
| `monitor` / `mitm-ports` / `set-cred` / `unset-cred` / `proxy-apply` | selective-mitm + credential control |
| `shell` | debug bash shell in the container |
| `rebuild [--destroy] [--destroy-inner]` | rebuild the sandbox image |
| `dashboard` / `dashboard stop` | the host-wide project dashboard |
| `version` / `help` | build info / usage |

## Architecture

Full detail in [`docs/architecture.md`](docs/architecture.md).

```
HOST                       SANDBOX (outer, --privileged)     INNER (DinD)
────                       ─────────────────────────────     ───────────
sandclaude CLI             claude (dummy token)              app containers
mitmweb (creds/TLS) ◀──── allowlist-proxy :3128 ───────────  (rails, pg, …)
web dashboard              enforces allowed-domains.txt              ▲
                           iptables PREROUTING ─▶ :3128 ────────────┘
```

- **mitm proxy** (host) — `mitmweb` terminates TLS and injects real credentials,
  so Claude only ever holds a dummy token.
- **allowlist-proxy** (sandbox, `:3128`) — every outbound connection, including
  inner DinD containers (captured via iptables PREROUTING), must pass it;
  non-allowlisted domains get `403`. Chains up to the host mitmweb.
- **web dashboard** (host) — live per-project view: terminal, mitm flows,
  firewall log, config.

## Dashboard

`sandclaude start`/`dev` auto-start a long-lived, host-wide web dashboard and open
your browser to the project's tab (live terminal, mitm flow table, firewall log,
config). Run it manually with `sandclaude dashboard` / `dashboard stop`.

**Loopback-only, token-gated.** Binds `127.0.0.1` only; every route needs a random
per-launch token (passed once in the URL, then an `HttpOnly` cookie). This matters
because the terminal tab grants a real shell — and for DinD projects that's a
near-direct path to host root — so the token also defends against DNS-rebinding
from another browser tab. Treat the printed URL like a credential.

Reopening resumes exactly where you left off: the dashboard is a thin stateless
viewer over already-persistent backends (tmux session, the mitmweb process, the
on-disk log). The terminal is a built-in PTY-over-WebSocket bridge (no `ttyd`) and
needs a `dev`-started tmux session to attach to.

## Docker-in-Docker

Enable during `init` (with optional `3000:3000`-style host port mappings). A full
inner `dockerd` runs in the sandbox; `DOCKER_HOST` points Claude's `docker` at it,
never the host socket.

```
sandclaude container (--privileged)
  ├── allowlist-proxy (:3128)
  ├── dockerd (unix:///var/run/dind/docker.sock)
  │     └── inner containers on 172.18.0.0/16
  └── iptables PREROUTING: 172.18.0.0/16 TCP -> :3128   (egress enforced)
```

Inner image builds trust the mitm CA automatically (the `bin/docker` wrapper
injects it per `FROM` stage), so `npm install` / `pip` / `bundle` / `go mod` work
over HTTPS through the proxy with no Dockerfile changes. `bin/cert-injector` does
the same for `docker run` containers.

Published inner ports reach the host through the outer container's `-p` mapping;
see the port chain in `docs/architecture.md`. Troubleshooting:

```bash
sandclaude start --dind-storage-driver=vfs   # if overlay2 fails ("operation not permitted")
sandclaude shell && cat logs/dockerd.log     # inner dockerd logs
sandclaude start --disable-dind              # skip DinD for one run
```

## Layout & state

| Path | Contents |
|---|---|
| `<prefix>/sandclaude` | the CLI binary |
| `~/.sandclaude/assets/{sandbox,host}/` | runtime asset bundle (override with `SANDCLAUDE_HOME`) |
| `~/.sandclaude/proxy-credentials.json` | global credentials (shared across projects) |
| `./.sandclaude/project/` | per-project config, `.allowlist-key`, optional creds override |
| `./.sandclaude/allowed-domains.txt[.enc]` | per-project firewall allowlist |
| `./.sandclaude/logs/` | proxy / mitm / cert-injector logs |

## Security

- Ephemeral container (`--rm`), runs as non-root matching host UID/GID
- Outbound network restricted to the allowlist (host + inner containers)
- Real credentials never enter the container — injected at the host proxy
- No host Docker socket mount; dashboard is loopback-only + token-gated

## Developing

Running `./sandclaude` from a checkout uses the checkout's `sandbox/` + `host/`
dirs as the asset root — no install needed:

```bash
go build -o sandclaude ./cmd/sandclaude
./sandclaude list
go test ./...                    # unit tests
cd tests/e2e && npm test         # full DinD e2e suite (privileged; also runs in CI)
```

Asset resolution (`AssetsDir`/`HostAssetsDir` in `internal/config/paths.go`):
`$SANDCLAUDE_HOME/assets/{sandbox,host}` → `<bindir>/assets/{sandbox,host}` →
`<bindir>/{sandbox,host}` (dev mode).

### Releasing

Tag a semver (`git tag v0.1.0 && git push origin v0.1.0`) — GitHub Actions runs
GoReleaser (`.goreleaser.yml`) to build the per-platform binaries + the
`sandclaude-assets.tar.gz` bundle and publish a GitHub Release. The
[install script](scripts/install.sh) downloads from there.

## License

TBD.
