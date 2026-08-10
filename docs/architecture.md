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

The dashboard never opens a network connection to a container — a container has
no listening port the dashboard dials (it lives in its own netns behind the
egress firewall). Every interactive surface is instead a **local child process**
the host `sandclaude` spawns, attached to a **PTY**, whose bytes are bridged over
a same-origin WebSocket to xterm.js in the browser. Control reaches the container
through host-side channels the operator already owns: the **Docker Engine socket**
and the **host tmux server**.

```
browser (xterm.js)                    HOST: sandclaude dashboard
  │  WebSocket /p/<id>/<kind>/ws                     │
  │  ── binary frame: keystrokes ───────────────▶ ptmx (PTY master)
  │  ◀─ binary frame: output ───────────────────── │        │
  │  ── {"type":"resize",cols,rows} (JSON) ──────▶ TIOCSWINSZ│
  │                                                          ▼ child process
  │                                          ┌───────────────────────────────┐
  │  terminal/ws  → tmux attach-session ─────┼─▶ Claude dev session (the very │
  │                 (host tmux server)        │   `docker run -it` PID)        │
  │  container/ws → docker exec -it <c> bash ─┼─▶ shell INSIDE the container   │
  │                 (Docker Engine socket)    │   (Docker demuxes the exec)    │
  │  host/ws      → tmux attach <id>-host ────┼─▶ shell ON the host, in the    │
  │                 (host tmux server)        │   project workspace            │
  └───────────────────────────────────────────┘
```

### The Claude terminal, byte by byte

The Claude terminal is the fullest version of the bridge, and it's worth tracing
end to end because it explains where tmux lives and why. There are **two** PTYs
stacked, and **tmux runs on the HOST** — not in the container:

```
xterm.js (browser)
   │  WebSocket — binary frames are raw terminal bytes (VT/ANSI + text)
   ▼
sandclaude dashboard (HOST)                      ── a dumb byte relay ──
   │  writes ▶ / reads ◀  PTY-A master
   ▼
PTY-A  (created by pty.Start; lives on the HOST)
   │  its slave is stdin/stdout of…
   ▼
tmux attach-session   ← the child process the dashboard spawned (a tmux CLIENT)
   │  talks to the tmux SERVER over tmux's own unix socket (not the dashboard's)
   ▼
tmux server → session → pane   (all on the HOST)
   │  the pane's command is `docker run -it … claude`, wired to pane PTY-B
   ▼
docker run -it   (HOST process: the docker client)
   │  -it plumbs the container's stdio to PTY-B
   ▼
claude   ← the ONLY piece running INSIDE the container
```

So the host runs tmux **and** the `docker run -it` client; only `claude` runs in
the container, with its terminal plumbed out through `-it` to the host tmux pane.

How the bytes move:

- **You type** → xterm.js sends a WS binary frame → the dashboard writes those
  exact bytes into PTY-A → they arrive as the tmux client's stdin → tmux forwards
  them to the focused pane → `claude` reads them on PTY-B.
- **Display** → `claude` writes to PTY-B → the tmux server composes the screen and
  the client emits a stream of **VT/ANSI escape codes** (cursor moves, colors,
  clears) + text to PTY-A → the dashboard reads PTY-A → WS binary frame → xterm.js.

The dashboard renders nothing and understands nothing — it shovels raw bytes both
ways. Correct display happens because **xterm.js is a terminal emulator**: it
parses the same VT/ANSI stream a physical terminal would and paints the character
grid. The one structured message is the `{"type":"resize"}` JSON frame, which the
dashboard turns into a `TIOCSWINSZ` on PTY-A so tmux lays the pane out to match
xterm.js's dimensions.

**Why tmux is in the middle** (the container shell skips it — `docker exec -it`
attaches PTY-A straight to a shell): tmux is the persistence layer that outlives
the connection. The tmux server keeps the session (and `claude`) running whether
or not a client is attached, so closing the browser tab just kills the
`tmux attach` client and tears down PTY-A — the session survives, and reattaching
spins up a fresh PTY-A and redraws the current screen + scrollback. This is the
same reason `sandclaude dev` can detach/reattach, and why the host shell got its
own `<id>-host` tmux session.

`bridgePTY` (internal/dashboard/terminal.go) is the shared bridge: it upgrades
the WebSocket, `pty.Start`s the given command, and pumps PTY↔WS both ways, with
a small JSON `{"type":"resize"}` control frame mapped to the PTY's window size.
The three project terminals differ only in the command handed to it:

- **Claude terminal** (`terminal/ws`) — `tmux attach-session` to the project's dev
  session. That session *is* the `docker run -it` process, so attaching mirrors
  the live Claude run and redraws current screen + scrollback; closing the tab
  detaches without killing it.
- **Container shell** (`container/ws`) — `docker exec -it <container> bash`, a
  fresh shell inside the sandbox for poking around its filesystem. Gated on the
  container running.
- **Host shell** (`host/ws`) — `tmux attach` to a per-project host-side session
  (`<id>-host`) so it persists across navigation and reloads (see the dashboard
  UI docs). Not sandboxed — it's a real shell on the host.

Non-terminal data (files, git diff, mitm flows, config, status) is plain
request/response, not a PTY — and mostly doesn't touch the container at all.
Because the project workspace is bind-mounted, the Files and Diff tabs read/write
the files and run `git` **directly on the host**; mitm flows and firewall/proxy
logs are read from the per-project `.sandclaude/logs/` on the host (mitmweb also
runs host-side). Container-scoped actions that do need it (e.g. restart) shell
out via the Docker socket. The **Ask Claude** chat and the **scoped-ssh-agent
load** ride the same WS-PTY bridge; the ssh-agent's own socket (above) is a
separate bind-mount, not a PTY.

All of this is loopback-bound and token-gated; the WebSocket upgrader is
same-origin only. A container never initiates any of these — it can neither reach
the dashboard nor open a session; the operator drives everything from the host.

Because the sessions are host tmux sessions, **`tmux` is a host dependency**: it
must be installed on the machine running sandclaude for `start`/`dev` and the
dashboard terminals to work (the installer sets it up alongside mitmproxy).

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

