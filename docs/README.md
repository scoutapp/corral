# Corral docs

Corral runs Claude Code in isolated, network-firewalled Docker sandboxes and gives
you a dashboard to drive them. For quick help while you're in the app, press
<kbd>⌘/</kbd> to open the docs drawer on any page.

## Start here

- [Using Corral](usage.md) — install, the CLI, and the end-to-end workflow.
- [Security model](security.md) — the trust boundary, in detail.
- [Architecture](architecture.md) — how the pieces fit (proxy, firewall, container).
- [Developing Corral](developers.md) — build from source, run the tests, and how to
  actually iterate on Corral itself.

## In-depth guides

Feature-by-feature "how do I actually do X" — short, with examples. These live under
[`internal/`](internal/):

- [Screenshots](internal/screenshots.md) — a visual tour of every dashboard page.
- [Flows](internal/flows.md) — chain steps into a job and schedule it.
- [Prompts](internal/prompts.md) — the project-start prompt, saved prompts, repo overrides.
- [DinD volumes & caches](internal/dind-volumes.md) — how inner-Docker data persists, and
  starting a project from a prebuilt cache.
- [Live View](internal/live-view.md) — watch a web app your sandbox is running.
- [Skills & context](internal/skills-and-context.md) — attach SKILL.md capabilities and a
  CLAUDE.md to a repo so every sandbox carries them.
- [Automations & hooks](internal/automations.md) — run your own steps when something happens.
- [Integrations (MCP)](internal/integrations.md) — connect MCP servers to the dashboard chat.
- [Config: network & firewall](internal/config-networking.md) — the allowlist, credentials,
  passthrough mode, ports.
- [Logs](internal/logs.md) — the host-wide activity log.

## The one rule worth knowing

Trust goes **one direction**: the host drives the sandbox, never the reverse. The
sandbox can't reach the dashboard, and real credentials never enter it — the host
proxy injects them into outbound requests. Everything else follows from that.
