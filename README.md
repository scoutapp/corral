# Sandclaude

Let Claude Code work on your project on autopilot — without worrying about what it
might touch. Sandclaude runs Claude with permission prompts turned off, but inside
a safe bubble: it can only reach the sites you allow, it never sees your real
credentials, and everything happens in a throwaway container that leaves your
machine untouched. You watch it work in a live dashboard in your browser.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/scoutapp/sandclaude/main/scripts/install.sh | bash
```

That's it — you just need [Docker](https://www.docker.com/) installed and Claude
Code signed in (`claude` once, to log in).

## Get going

```bash
cd ~/my-project
sandclaude init                                     # answer a few prompts, once per project
sandclaude populate-proxy-credentials               # set your credentials once
sandclaude start --passthrough-firewall-and-write   # start working
```

During `init` you'll be asked a few yes/no questions (protect my credentials?
let Claude use Docker? expose any ports?) — the recommended answers are the
defaults. Your credentials are set once and reused across every project.

**Start with `--passthrough-firewall-and-write`.** A brand-new project has an empty
allowlist, so a plain `start` would block the sites your project needs. Passthrough
lets everything through *and quietly records what gets used* — so Claude can work
right away while Sandclaude learns your project's real network needs. (The first
`start` sets things up and can take a minute; after that it's quick.)

Once things are working, lock it down to just what was actually used:

```bash
sandclaude firewall-reload   # lock in the discovered sites
sandclaude start             # now enforced — nothing new gets out
```

## Watching & driving

`start` opens a browser dashboard where you can watch Claude live, see what it's
reaching out to, and drop into a terminal. One dashboard covers **all** your
projects at once — run Claude across several repos and watch them side by side
from a single page. It's private to your machine and requires the link it prints —
treat that link like a password.

Prefer the terminal? `sandclaude dev` runs Claude in the background so you can
`capture` its output, `send` it a prompt, or `attach` to it directly.

**Want the full tour?** See the [usage guide](docs/usage.md) — the dashboard, the
project tabs, and everything you can drive from your browser (with screenshots).

## What keeps it safe

- **Nothing leaks.** Claude runs with a dummy token; your real credentials are
  swapped in behind the scenes and never enter its environment.
- **Nothing unexpected gets out.** Outbound network is limited to the sites you've
  allowed — including anything Claude spins up in Docker.
- **Nothing sticks around.** It all runs in a disposable container; close it and
  your machine is as it was.
- **You're always watching.** The live dashboard shows exactly what's happening.

For the full trust model — including the deliberate trade-offs and residual risks
(the privileged DinD container, loopback reachability, dangerous mode) — see
[`docs/security.md`](docs/security.md).

## Commands

| Command | What it does |
|---|---|
| `init` / `update` / `remove` | set up or change a project |
| `start` | start Claude + open the dashboard |
| `start --foreground` | run it right in your terminal instead |
| `dev` | run in the background (`capture` / `send` / `attach`) |
| `list` | show this project's settings |
| `dashboard` | open the dashboard on its own |
| `firewall-reload` / `firewall-monitor` | update / watch what's allowed out |
| `populate-proxy-credentials` | set your credentials (add `--project` for a per-project set) |
| `shell` | open a shell inside the sandbox |
| `rebuild` | rebuild the sandbox from scratch |
| `version` / `help` | version info / full command list |

Run `sandclaude help` for the complete list and options.

## Good to know

- Everything for a project lives in `./.sandclaude/` (safe to delete to start over;
  `init` git-ignores it for you).
- Shared settings and credentials live in `~/.sandclaude/`.
- Pin a version or change install locations with `SANDCLAUDE_VERSION`,
  `SANDCLAUDE_PREFIX`, `SANDCLAUDE_HOME` before the install command.

## For developers

Curious how it works under the hood, or want to hack on Sandclaude itself? See
[`docs/architecture.md`](docs/architecture.md) for the design, and:

```bash
git clone https://github.com/scoutapp/sandclaude.git && cd sandclaude
./install.sh                     # build + install from source
go build -o sandclaude ./cmd/sandclaude && ./sandclaude list   # or run from the checkout
go test ./... && (cd tests/e2e && npm test)                    # tests (e2e also runs in CI)
```

Releases are cut by tagging a version (`git tag v0.1.0 && git push origin v0.1.0`);
GitHub Actions builds the binaries and the installer downloads them.

## License

[MIT](LICENSE) © Scout Monitoring
