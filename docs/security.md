# Security model

Sandclaude runs Claude Code in **dangerous mode** (no permission prompts) but
inside a sandbox: an ephemeral container whose outbound network is restricted to
an allowlist and whose real credentials it never sees. This document is honest
about what that boundary does and does **not** protect against, so you can decide
what's acceptable for your threat model.

## What's protected

- **Credentials.** In proxy mode, Claude gets a dummy Claude token; the host-side
  `mitmweb` proxy injects the real credentials into outbound requests. Claude
  never holds your real Anthropic token.
- **Outbound network.** All egress — from the outer container *and* inner
  Docker-in-Docker containers — is forced through the in-sandbox allowlist proxy
  (explicit `HTTP_PROXY` for the outer container's own processes; transparent
  iptables `PREROUTING` capture for inner containers). Non-allowlisted domains
  get a `403`.
- **The workspace, not your whole machine.** Only the project workspace directory
  is bind-mounted into the container. Your home directory, SSH keys, etc. are not
  mounted.
- **The dashboard is loopback-only + token-gated.** It binds `127.0.0.1` only and
  every route requires a random per-launch token (then an `HttpOnly` cookie).

## Residual risks (know these)

These are real and mostly deliberate trade-offs, not bugs.

### 1. The outer container is privileged and in the `docker` group
Docker-in-Docker requires the outer container to run `--privileged`, and the
`claude` user is added to the `docker` group so it can drive the inner daemon. A
`--privileged` container with Docker access is, by design, close to root on the
host's Docker context. **A sandbox escape from this container is not a high bar** —
treat "Claude has the outer container" as a meaningful capability, not an
airtight jail. DinD is the reason; running with `--disable-dind` avoids the
privileged flag.

### 2. GitHub token, unlike the Anthropic token, is currently the real token
The Anthropic token is handled well: in proxy mode Claude gets a **dummy**
`CLAUDE_CODE_OAUTH_TOKEN` and the proxy injects the real one, so Claude never sees
it. **GitHub is not (yet) handled the same way.** On the current codebase the real
host GitHub token (`gh auth token`) is passed into the container as `GH_TOKEN`
**unconditionally — including in proxy mode** — so Claude can read it directly
(`echo $GH_TOKEN`, `gh auth token`). This is the one credential the proxy does not
shield today.

A separate change (open PR) brings GitHub in line with Anthropic: inject a
**dummy** `GH_TOKEN` (so `gh`/`git` still work without prompting) while the real
token is injected only at the proxy, plus a `gh` wrapper that blocks the
token-revealing commands. Until that merges, assume any GitHub token you
authenticate with is visible to Claude.

### 3. Loopback services are reachable from the container → the token is the guard
The container reaches the host over `host.docker.internal` (mapped to the host
gateway) to talk to the mitmweb proxy. That same path means container processes —
and inner DinD containers — **can in principle reach other host loopback
services**, including the dashboard. The dashboard's **per-launch token is what
prevents that access**: without the token, a request from the container (or a
DNS-rebinding attempt from a browser tab) can't drive it. Treat the printed
dashboard URL/token like a credential; if it leaks, the loopback protection alone
is not enough.

### 4. The dashboard exposes shells and a write path to your code
The dashboard can open an interactive shell **in the container** (near-root for a
privileged DinD project), a shell **on the host** (the operator's own shell, in
the workspace dir), and can **edit/save workspace files**. These are gated only
by the loopback bind + token. They surface capabilities that already exist
(Claude edits the workspace; `sandclaude shell` opens a container shell), but they
do raise the stakes of a token compromise. WebSocket endpoints additionally
enforce a same-origin check.

### 5. Passthrough / disabled firewall
`--passthrough-firewall-and-write` allows *all* outbound traffic (logging new
domains), and `--disable-firewall` removes egress enforcement entirely. Both are
intended for bootstrapping a new project's allowlist — while active, the network
containment described above does not apply.

### 6. Dangerous mode
Claude runs with `--dangerously-skip-permissions` — it executes commands without
asking. The container + firewall are the guardrails; there is no per-action
approval. Everything above is what stands between "Claude ran a command" and
"that command affected your machine or network."

## Practical guidance

- Keep the credential proxy **on** (the default recommendation at `init`) — it's
  the difference between Claude holding your real tokens or dummies.
- Treat the dashboard URL/token as secret.
- Prefer `--disable-dind` when a project doesn't need inner containers — it drops
  the privileged flag and shrinks the outer container's blast radius.
- Don't point sandclaude at a workspace containing secrets you don't want Claude
  to read; only the workspace is mounted, but it *is* fully readable/writable.
