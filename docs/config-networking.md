# Config: network & firewall

Every sandbox runs behind a network firewall — outbound connections are restricted
to an allowlist, and real credentials never enter the box. A project's **Config**
tab is where you tune this. Most changes here are restart-required.

## The allowlist

The sandbox can only reach approved domains. Common ones (npm, GitHub, the Anthropic
API, Go/Rust/Python registries) are preloaded. Add a domain to the project's
allowlist and reload:

```bash
# from the host, in the project
echo 'rubygems.org' >> .corral/allowed-domains.txt
corral firewall-reload
```

Blocked requests are visible in the project's **Firewall Log** tab.

## Credentials — injected, never stored

Real tokens stay on the host. The proxy injects them into outbound requests, so the
sandbox uses `gh`/`git` normally without a readable token ever inside it. Add
per-host credentials in **Config → Credentials**.

## Passthrough mode

The default is "permissive but observed": the proxy still inspects traffic and
injects credentials, but **unknown domains are allowed and logged** instead of
blocked, and direct TCP (git-over-SSH) works. Turn it off (**Config → enforce
allowlist**) for a strict, block-by-default firewall.

## Ports

With Docker-in-Docker on, **published ports** map inner services to your host. For
watching a web app, you usually don't need these — see [Live View](live-view.md).

## Gotchas

- Network, DinD, ports, SSH keys, and cache are **restart-required** — editing them
  prompts you to restart the project.
- Allowlist and credential changes hot-reload the proxy; no restart.
