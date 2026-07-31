# Security model

Sandclaude runs Claude in **dangerous mode** (no permission prompts) inside a
sandbox: an ephemeral container with allowlisted egress and injected credentials.
Here's what that boundary does and doesn't cover.

## Protected

- **Anthropic credentials** — in proxy mode Claude gets a dummy token; the host
  proxy injects the real one. Claude never sees it.
- **GitHub credentials** — same treatment: in proxy mode `GH_TOKEN` is a dummy,
  the proxy injects the real token into `api.github.com` requests, and an
  in-container `gh` wrapper blocks the token-revealing subcommands
  (`gh auth token`, `gh auth status --show-token`). `gh`/`git` still work; neither
  Claude nor a shell command can extract the real token.
- **Outbound network** — all egress (outer container *and* inner DinD containers)
  is forced through the allowlist proxy; non-allowlisted domains get `403`.
- **Filesystem** — only the project workspace is mounted, not your home/SSH keys.
- **Dashboard** — binds `127.0.0.1` only, every route requires a per-launch token.

## What "host privileges" means (Linux vs macOS)

Several risks below say something runs with "the operator's full host privileges"
or that container escape is "a real capability." What that blast radius actually
is depends on your host:

- **Linux host.** The container shares the host kernel. `--privileged` + `claude`
  in the `docker` group is **close to root on your actual machine**: an escape (or
  just `docker run --privileged -v /:/host …` via the docker socket) reaches the
  real filesystem, devices, and other processes. The host-side pieces (dashboard,
  host shell, host `claude`) run as your Linux user with your full privileges.
  Treat "escape" as "root on this box."
- **macOS host.** Docker runs inside a **Linux VM** (Docker Desktop / colima), not
  on macOS directly. `--privileged` and container escape get you root **inside
  that VM** — not on macOS. The VM is a meaningful boundary: it does *not* see your
  macOS processes or the whole macOS filesystem. BUT two caveats: (1) any path you
  **mount** (the workspace, and anything the VM bind-mounts) *is* reachable, and
  (2) the **host-side** pieces — the dashboard server, host shell, and host
  `claude` — still run as your **macOS user** with full macOS privileges, entirely
  outside the VM. So on macOS the *container* blast radius is smaller (VM-scoped),
  but the *host-side dashboard features* are exactly as privileged as on Linux.

In short: the container is better contained on macOS (VM) than Linux (shared
kernel); the host-side dashboard/chat/shell trust model is the same on both.

## Residual risks

1. **Privileged outer container.** DinD needs `--privileged` and puts `claude` in
   the `docker` group — close to host root (see the platform note above: real root
   on Linux, root in the Docker VM on macOS). Escape is not a high bar; treat it as
   a real capability. `--disable-dind` drops the privileged flag.
2. **Loopback is reachable → the token is the guard.** The container (and inner
   containers) can reach host loopback services, including the dashboard. The
   per-launch token is what prevents access — treat the dashboard URL as a secret.
3. **Dashboard grants shells + writes.** It can open a container shell, a *host*
   shell, and edit workspace files — gated only by loopback + token.
   The dashboard can also **create projects host-side**: cloning repos (private
   ones use your ambient host git/`gh` credentials — no tokens are stored) into
   `~/.sandclaude/workspaces/`, and **starting** a project's container by running
   `sandclaude dev` on the host. These run with the operator's full host
   privileges (same loopback + token trust basis as the host shell). Adding a repo
   clones an operator-supplied URL — an outbound host action by design.
4. **"Ask Claude" chat panel is NOT sandboxed.** The dashboard chat panel runs
   the operator's *own host* `claude` (real credentials/subscription — not the
   Anthropic API, not the sandboxed container instance), started in the project
   workspace with the operator's full host privileges. It defaults to a read-only
   tool set (`Read`/`Grep`/`Glob`, validated server-side against a whitelist), but
   granting `Bash`/`Edit`/`Write` lets it act on the host directly. Same trust
   basis as the host shell (loopback + token); the panel shows a persistent
   "not sandboxed" warning to make this explicit to the user.
5. **Dashboard editor bundle is trusted host-side code.** The CodeMirror editor
   bundle is built at dev time from npm packages, committed, and `go:embed`-ed
   into the host binary — it runs in the operator's browser served by the *host*
   dashboard, outside the sandbox. Dependencies are exact-pinned (lockfile
   authoritative) and no npm runs at install/deploy time (the frozen bundle is the
   shipped artifact), so the supply-chain exposure is at *build* time, not deploy
   time. One grammar (`codemirror-lang-elixir`) is from a now-archived upstream,
   frozen at a pinned version. Rebuild deliberately and review the bundle diff.
6. **Passthrough / `--disable-firewall`** turn off egress containment (for
   bootstrapping an allowlist); no network protection while active.
7. **Dangerous mode** — no per-action approval; the container + firewall are the
   only guardrails.

## Guidance

- Keep the credential proxy **on** (the `init` default).
- Treat the dashboard URL/token as a secret.
- Use `--disable-dind` when you don't need inner containers.
- Don't mount a workspace holding secrets you don't want Claude to read.
