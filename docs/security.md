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

## Residual risks

1. **Privileged outer container.** DinD needs `--privileged` and puts `claude` in
   the `docker` group — close to host root. Escape is not a high bar; treat it as
   a real capability. `--disable-dind` drops the privileged flag.
2. **Loopback is reachable → the token is the guard.** The container (and inner
   containers) can reach host loopback services, including the dashboard. The
   per-launch token is what prevents access — treat the dashboard URL as a secret.
3. **Dashboard grants shells + writes.** It can open a container shell, a *host*
   shell, and edit workspace files — gated only by loopback + token.
4. **Passthrough / `--disable-firewall`** turn off egress containment (for
   bootstrapping an allowlist); no network protection while active.
5. **Dangerous mode** — no per-action approval; the container + firewall are the
   only guardrails.

## Guidance

- Keep the credential proxy **on** (the `init` default).
- Treat the dashboard URL/token as a secret.
- Use `--disable-dind` when you don't need inner containers.
- Don't mount a workspace holding secrets you don't want Claude to read.
