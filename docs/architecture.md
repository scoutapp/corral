# Architecture

Four tiers. Traffic and control flow between them shown below.

```
┌──────────────────────────────── HOST (macOS) ─────────────────────────────────┐
│                                                                                │
│   sandclaude (Go binary)                                                       │
│     ├─ starts mitmweb ──────────┐        ┌─ serves Web Dashboard :PORT         │
│     │    -s proxy-addon.py      │        │    (projects, mitm flows, terminal, │
│     │    (host mitm :9500+)     │        │     config) ── browser opens tab    │
│     ├─ scoped ssh-agent ──┐     │        └─ SSE/WS ⇄ tmux in sandbox           │
│     │    (per project;    │     │                                              │
│     │     holds only the  │     │                                              │
│     │     chosen keys)    │     │                                              │
│     └─ docker run ──▶ sandbox   │                                              │
│                           │     │                                              │
│   ┌──────────────── SANDBOX (outer container) ─────────────┐   ▲ TLS traffic  │
│   │                       │ agent socket bind-mount        │   │ decrypted     │
│   │                       ▼ (SSH_AUTH_SOCK; keys usable,   │   │ + credential  │
│   │  entrypoint.sh (PID 1)   never readable)               │   │ injection     │
│   │  launcher.py → claude (tmux session)                   │   │               │
│   │                                                        │   │               │
│   │  allowlist-proxy  ──chains upstream──────────────────────┘  by proxy-addon │
│   │    (enforces allowed-domains.txt; blocks the rest)     │                   │
│   │       ▲                                                │                   │
│   │       │ HTTP(S)_PROXY                                  │                   │
│   │  ┌────┴───────── DinD (docker-in-docker) ───────────┐  │                   │
│   │  │                                                  │  │                   │
│   │  │  inner app containers  (agent builds/runs these) │  │                   │
│   │  │     ▲                    ▲                        │  │                   │
│   │  │     │ cert cp+restart    │ rewritten Dockerfile   │  │                   │
│   │  └─────┼────────────────────┼────────────────────────┘ │                   │
│   │   bin/cert-injector    bin/docker (build wrapper)      │                   │
│   │   (daemon: injects     (injects mitm CA into every     │                   │
│   │    mitm CA on           FROM stage so in-build          │                   │
│   │    create/start)        HTTPS installs trust the proxy) │                   │
│   └────────────────────────────────────────────────────────┘                  │
└────────────────────────────────────────────────────────────────────────────────┘

Legend
  host mitmweb      Terminates TLS, injects credentials, records flows (dashboard reads these)
  allowlist-proxy   In-sandbox gatekeeper; only allowed domains pass, chains up to host mitm
  scoped ssh-agent  Per-project host agent holding only the chosen keys; its socket is
                    bind-mounted in so the container can SIGN but never read the key bytes
  DinD              Nested docker; agent's app containers run isolated here
  cert-injector     Makes inner containers trust the mitm CA (docker cp + one restart)
  bin/docker        Wraps `docker build` to trust the mitm CA during image builds
```

## Flow: an inner container makes an HTTPS request


```
inner app ─▶ allowlist-proxy ─(if allowed)─▶ host mitmweb ─▶ internet
   │            │                                 │
   │            └─ blocks if domain not in        └─ decrypts, injects creds,
   │               allowed-domains.txt               logs flow → dashboard
   └─ trusts mitm CA (injected by cert-injector / bin/docker)
```

## Flow: the agent uses an SSH key (git push, clone over SSH)

Keys never enter the sandbox. Instead, a per-project ssh-agent runs on the HOST
holding only the keys chosen for that project, and only its socket is mounted in:

```
                     HOST                          │        SANDBOX
  chosen keys (union: global ∪ project)            │
        │  ssh-add (passphrase typed once,         │
        ▼   via foreground shell or dashboard PTY) │
  scoped ssh-agent ──── agent.sock ────────────────┼──▶ /ssh-agent.sock
  (~/.sandclaude/agents/<projectID>/)   bind-mount  │     (SSH_AUTH_SOCK)
        ▲                                           │        │
        └─────────── sign request ─────────────────┼────────┘  git / ssh in the
                     (no key bytes cross)           │           container asks the
                                                    │           agent to sign
```

Why a scoped agent rather than forwarding the host's real agent, or mounting the
key file:

- **Scoping** — the agent holds ONLY this project's keys, not everything in your
  real agent. Key selection is the union of the global default set
  (`~/.sandclaude/ssh-keys.json`) and the project's extras (see
  `ProjectConfig.ResolveSSHKeys`).
- **No byte leak** — the ssh-agent protocol has no "export key" operation, so even
  an escaped container can request signatures but cannot copy the private key.
  No key file is ever bind-mounted.
- **Lifetime** — the agent is tied to the container: created on start, torn down on
  stop. Keys live only in the agent's memory; a restart re-prompts (or, on macOS,
  reloads silently from the login Keychain if the passphrase was saved). Loading is
  interactive because keys are passphrase-protected — it runs in a PTY the caller
  owns (the foreground shell, or the dashboard's host-terminal). See
  [`docs/security.md`](security.md) and the `internal/ssh` package docs.

macOS-first: the socket lives under `~/.sandclaude` (a Docker-Desktop shared path)
so virtiofs proxies the Unix-socket connection host → VM → container. Linux works
too; its cross-project residual risk differs and is noted in the code.

## How the dashboard reaches a container

The dashboard never connects *to* a container — it's sealed off (no network in),
so there's nothing to dial. The host drives everything: it runs a helper command
wired to a PTY and bridges that PTY to xterm.js in the browser over a WebSocket.
`bridgePTY` (internal/dashboard/terminal.go) is that shared bridge — it just
shovels bytes both ways (plus a `{"type":"resize"}` frame → PTY window size);
xterm.js does the rendering. The three terminals differ only in the command:

- **Claude terminal** (`terminal/ws`) — `tmux attach` to the dev session.
- **Container shell** (`container/ws`) — `docker exec -it` a shell in the sandbox.
- **Host shell** (`host/ws`) — `tmux attach` to a per-project `<id>-host` session;
  not sandboxed, a real shell on the host.

The Claude terminal is the fullest path — two stacked PTYs, with tmux and the
docker client both on the host and only `claude` in the container:

```
xterm.js (browser)
   ▼  WebSocket (raw terminal bytes)
dashboard (host) ── relays bytes, renders nothing
   ▼  PTY-A
tmux attach  (client → server → pane, all on the host)
   ▼  PTY-B (the pane's command)
docker run -it  (host: docker client; -it plumbs the container's stdio to PTY-B)
   ▼
claude  (the only piece INSIDE the container)
```

tmux sits in the middle so the session outlives the connection: closing the tab
kills the `tmux attach` client, but the server keeps `claude` running — reattaching
redraws the screen + scrollback. Same reason `sandclaude dev` can detach/reattach.

Non-terminal data (files, git diff, mitm flows) mostly skips the container: the
workspace is bind-mounted, so Files/Diff run on the host and logs are read there
too. Everything is loopback-bound, token-gated, and same-origin. Because the
sessions are host tmux sessions, **tmux is a host dependency** — the installer
sets it up alongside mitmproxy.

## Where things live (repo layout by tier)

```
cmd/sandclaude/        host CLI entrypoint (Go)
internal/              host CLI packages: config, creds, session, proxy,
                       dashboard, container, cli
  ssh/                   per-project scoped ssh-agent (start/adopt, key load,
                         teardown); socket lives under ~/.sandclaude/agents/
host/                  HOST-tier assets loaded by host processes
  proxy-addon.py         → loaded by the host's mitmweb
sandbox/               SANDBOX image build context + runtime mounts
  Dockerfile             builds the sandbox image
  entrypoint.sh          sandbox PID 1
  launcher.py            launches claude in the sandbox
  allowlist-proxy/       in-sandbox gatekeeper (own Go module)
  skills/                agent skills → mounted at ~/.claude/skills
  setup/                 sandbox self-config → /home/claude/bin (flattened)
    patch-claude-settings.py, statusline.sh, enforce-small-commits.sh
  dind/                  sandbox→inner bridge → /home/claude/bin (flattened)
    docker (build wrapper), cert-injector (CA daemon)
```

Installed layout mirrors this under `~/.sandclaude/assets/{sandbox,host}/`.

