# Architecture

Four tiers. Traffic and control flow between them shown below.

```
┌──────────────────────────────── HOST (macOS) ─────────────────────────────────┐
│                                                                                │
│   sandclaude (Go binary)                                                       │
│     ├─ starts mitmweb ──────────┐        ┌─ serves Web Dashboard :PORT         │
│     │    -s proxy-addon.py      │        │    (projects, mitm flows, terminal, │
│     │    (host mitm :9500+)     │        │     config) ── browser opens tab    │
│     └─ docker run ──▶ sandbox   │        └─ SSE/WS ⇄ tmux in sandbox           │
│                                 │                                              │
│   ┌──────────────── SANDBOX (outer container) ─────────────┐   ▲ TLS traffic  │
│   │                                                        │   │ decrypted     │
│   │  entrypoint.sh (PID 1)                                 │   │ + credential  │
│   │  launcher.py → claude (tmux session)                   │   │ injection     │
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
