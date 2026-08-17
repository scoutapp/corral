# Corral docs

Corral runs Claude Code in isolated, network-firewalled Docker sandboxes and gives
you a dashboard to drive them. These guides are the "how do I actually do X" —
short, with examples. For quick help while you're in the app, press <kbd>⌘/</kbd>
to open the docs drawer on any page.

## Guides

- [Flows](flows.md) — chain steps into a job and schedule it.
- [Prompts](prompts.md) — the project-start prompt, saved prompts, repo overrides.
- [DinD volumes & caches](dind-volumes.md) — how inner-Docker data persists, and
  starting a project from a prebuilt cache.
- [Live View](live-view.md) — watch a web app your sandbox is running.
- [Skills & context](skills-and-context.md) — attach SKILL.md capabilities and a
  CLAUDE.md to a repo so every sandbox carries them.
- [Automations & hooks](automations.md) — run your own steps when something happens.
- [Integrations (MCP)](integrations.md) — connect MCP servers to the dashboard chat.
- [Config: network & firewall](config-networking.md) — the allowlist, credentials,
  passthrough mode, ports.
- [Logs](logs.md) — the host-wide activity log.

## Going deeper

- [Using Corral](usage.md) — the CLI and end-to-end workflow.
- [Architecture](architecture.md) — how the pieces fit (proxy, firewall, container).
- [Security model](security.md) — the trust boundary, in detail.

## The one rule worth knowing

Trust goes **one direction**: the host drives the sandbox, never the reverse. The
sandbox can't reach the dashboard, and real credentials never enter it — the host
proxy injects them into outbound requests. Everything else follows from that.
