# Screenshots

A visual tour of the corral dashboard. (Captured against a throwaway demo repo.)
The same screenshots appear in the in-app docs drawer — press **⌘/** anywhere,
then pick a page from the sidebar.

## Projects

The home screen — one pane per sandboxed project. Each project is a repo checkout
running Claude Code in an isolated, network-firewalled Docker sandbox.

![Projects](img/projects.png)

## Repo

A repo corral tracks — PR review, issues, projects spun off it, forensics, and
per-repo skills/context + automations.

![Repo](img/repo.png)

## PR Review + AI analysis

Corral's read on one PR: the risk verdict, per-block AI analysis, churn/forensics,
and a merge split-button (host / sandbox / plain).

![PR Review](img/pr-review.png)

## Global settings

Host-wide defaults — SSH keys, global automations, PR-merging defaults.

![Global settings](img/global.png)

## Automations

Prompts, event hooks, flows, and saved scripts.

![Automations](img/automations.png)

## Run Log

A history of automation runs.

![Run Log](img/run-log.png)

## Logs

The app-wide searchable activity log, with distributed traces.

![Logs](img/logs.png)

## Integrations

Connect MCP servers (host-only — the sandbox never reaches these).

![Integrations](img/integrations.png)

## Ask Claude — the global chat as a conductor

Ask the app-wide chat to review PRs, start them in a sandbox to verify, and merge
the good ones. It acts as a *conductor* — delegating each PR to a fresh worker
Claude shown in the **Work** tab.

![Global chat reviewing PRs](img/global-chat.png)

## Docs drawer

The in-app docs (⌘/) — a sidebar to browse every page's docs, screenshots
included, no matter which page you're on.

![Docs drawer](img/docs-drawer.png)
