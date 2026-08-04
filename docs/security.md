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
  is forced through the allowlist proxy; non-allowlisted domains get `403`. This
  containment is unchanged by the capture preset below — *which* allowed traffic
  is decrypted (MITM'd) for inspection/credential-injection defaults to **minimal
  (Claude + GitHub)**; other allowed hosts still pass but are direct-dialed (not
  decrypted). Change it per project (Config → Capture): minimal / all / none / custom.
- **Filesystem** — only the project workspace is mounted, not your home/SSH keys.
- **SSH keys** — opt-in. You choose keys with a checklist (a global default set
  in Global settings, plus per-project extras in each project's Config tab; the
  effective set is the union). When any are chosen, sandclaude runs a *scoped*
  ssh-agent holding **only those keys** and bind-mounts just its **socket** (not
  any key file). The container can *use* the keys (sign, push) but never sees the
  private-key bytes — the agent protocol has no "export key" operation, so this is
  `:ro`-proof (there's no file to read). Your real host agent (and any keys in it
  you didn't choose) is never forwarded. Keys live only in the agent's memory and
  are torn down with the container. See "SSH residual risk" below.
- **Dashboard** — binds `127.0.0.1` only, every route requires a per-launch token.

## What "host privileges" means

**On macOS this is largely contained:** Docker runs in a throwaway Linux VM, so
even a full container escape is root *in that VM*, **not** on your Mac. On
**Linux** it matters more — the container shares the host kernel, so an escape is
closer to root on the machine itself.

Why escape is even a concern: with DinD on, the container runs **`--privileged`**
and `claude` is in the `docker` group (near-root). **`--disable-dind` drops
`--privileged`** — the container then gets only `NET_ADMIN`/`NET_RAW` for the
firewall, a much smaller footprint.

Either way, mounted paths are within reach: given that near-root access, `:ro`
mounts aren't a barrier — assume it can read/write anything mounted, including the
workspace. Don't mount secrets you don't want it to see.

(The host shell and the host-`claude` chat panel are separate — see residual
risks. The chat panel is read-only by default.)

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
   time. Rebuild deliberately and review the bundle diff.
6. **Passthrough / `--disable-firewall`** turn off egress containment (for
   bootstrapping an allowlist); no network protection while active.
7. **Dangerous mode** — no per-action approval; the container + firewall are the
   only guardrails.
8. **Seccomp `unconfined`** (Config → Seccomp) removes syscall filtering — needed
   by some runtimes (e.g. Erlang/BEAM) but it widens the container's attack
   surface. Same platform split as above: syscalls hit your **real kernel on
   Linux** (so `unconfined` genuinely widens host exposure), but only the **Docker
   VM's kernel on macOS** (contained by the VM). Default keeps Docker's profile.
   No effect with DinD (`--privileged` already disables seccomp).
9. **SSH scoped-agent — `use`, not `steal`, and a DinD cross-project window.**
   The container can *use* a chosen key while it's running (push, auth anywhere
   that key is trusted) — that's the point. It can't copy the key out (socket
   only, no bytes). Two bounded caveats:
   - **Usage window.** While the container is up, anything in it (including a shell
     you open) can use the chosen keys. Only load keys the project needs; they're
     torn down when the container stops.
   - **DinD cross-project (macOS).** The agent sockets live under `~/.sandclaude`
     (a Docker-shared path). With **DinD on** (`--privileged`), a container escape
     into the Docker VM can reach *another* running project's scoped agent socket
     and *use* its keys — still no byte theft, still confined to the VM (your Mac's
     real agent/keys are across the VM boundary, untouched). Bounded by
     container-lifetime teardown (an idle project has no live agent) and closed by
     `--disable-dind`. Linux (escape hits the real host) is out of scope for now.

## Guidance

- Keep the credential proxy **on** (the `init` default).
- Treat the dashboard URL/token as a secret.
- Use `--disable-dind` when you don't need inner containers.
- Don't mount a workspace holding secrets you don't want Claude to read.
- Give each project only the SSH keys it needs (Config → SSH keys); avoid running
  multiple key-bearing projects under DinD at the same time.
