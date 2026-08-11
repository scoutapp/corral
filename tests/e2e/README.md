# corral e2e suite

End-to-end Playwright/TypeScript tests that exercise the full `corral`
chain against a **real, privileged sandbox** — no mocks:

```
go build corral
  └─ corral init      (proxy on, DinD on, ports 3000:3000, tmux off)
      └─ corral start (detached, privileged outer container + inner dockerd)
          └─ docker build the fixture web app INSIDE the inner DinD
              └─ docker run it, publish :3000 on the DinD bridge
                  └─ socat-bridge outer :3000 → inner gateway :3000
                      └─ host GET localhost:3000  ==>  "corral e2e ok"
```

It must pass on **macOS** (Docker Desktop, local dev) and **Linux**
(GitHub Actions `ubuntu-latest`). The only platform-specific piece — the inner
DinD → outer interface bridge — is handled *inside* the Linux container by
`socat`, so the host assertion (`GET localhost:3000`) is identical on both.

## Run it locally (macOS)

Prerequisites:

- **Docker running** (Docker Desktop on macOS). The sandbox runs the outer
  container `--privileged` (required for DinD), which the CLI passes itself — no
  special Docker config needed.
- **Go** (1.22+) on `PATH` — global-setup builds the binary.
- **Node 18+** and **npm**.

```bash
cd tests/e2e
npm install
npx playwright install chromium
npm test
```

A **cold** run is slow: it builds the Go binary, builds the `corral-stable`
Docker image, boots a privileged container + inner dockerd, then builds and runs
a `node:20-slim` image inside DinD. Timeouts are sized for this (per-test 300s;
the `start` step allows up to 15 min for the first image build).

Useful:

```bash
npm run typecheck        # tsc --noEmit over the whole suite
npm run list             # playwright test --list (discover specs without running)
npx playwright test tests/chain.spec.ts   # just the orchestration chain
npx playwright show-report playwright-report
```

## What each spec covers

- **`tests/chain.spec.ts`** (serial, no browser — pure harness):
  1. *inner image builds inside DinD* — `docker cp` the fixture into the outer
     container, then build it in the inner daemon **through the sandbox's
     `bin/docker` wrapper** (the wrapper injects the mitmproxy CA so the
     fixture's `npm install` over the allowlist proxy is trusted; the raw
     `docker` binary would bypass that). Asserts a clean build and that the image
     is listed.
  2. *host reaches inner :3000* — `docker run -d -p 3000:3000` the image in the
     inner daemon, install the socat bridge, then `GET localhost:3000` returns
     `200` + `corral e2e ok`, and `/healthz` returns `{"status":"ok"}`.
  3. *logs and boot evidence* — asserts the captured `corral start` stdout
     mentions DinD + the port mapping, and that the in-container logs that this
     config actually produces exist: `dockerd.log`, `cert-injector.log`,
     `proxy.log` (all read via `readInnerLog`).

- **`tests/dashboard.spec.ts`** (Playwright/Chromium):
  - Parses the URL+token from `corral dashboard`.
  - Landing page: `200`, `<title>corral — control</title>`, brand marker.
  - `/static/dashboard.css` served `200`.
  - `/status` JSON lists this suite's project by basename; `container_up` true.
  - Project page `/p/<id>`: `200`, title contains the workspace, `data-project-id`
    matches, Config + Mitm tabs present.
  - `/p/<id>/config` returns JSON containing the workspace path.
  - *mitm flows* test is intentionally `skip`ped — the flow table only fills once
    intercepted traffic has flowed, and the suite never drives Claude. It's left
    as a documented placeholder rather than a flaky assertion.

## Chosen configuration (and why)

`init` is driven non-interactively with **proxy ON, DinD ON, `dind_ports`
`3000:3000`, tmux OFF**. global-setup pipes the interactive answers **and** then
overwrites `.corral/project/config.json` with those exact fields for
determinism.

**Why proxy on, not `--disable-firewall`:** when DinD is enabled the entrypoint
always installs a `PREROUTING REDIRECT` sending inner-container external TCP to
the transparent allowlist proxy on `:3129`. That redirect is applied regardless
of the firewall flag. With the firewall *disabled*, nothing listens on `:3129`,
so the fixture's `npm install` (needs `registry.npmjs.org`) would be redirected
into a dead port and fail. Proxy-on is the only config in which the inner build
can reach the network. `registry.npmjs.org` is already in the default allowlist,
and the mitmproxy CA is auto-generated on proxy start and injected into the build
by `bin/docker`. Proxy-on needs **no real Claude credentials** to start (a dummy
token is injected); the suite never drives Claude.

## Logs this config produces

- In-container (`<workspace>/.corral/logs/`, mounted from the outer
  container's `/home/claude/logs/`, read via `readInnerLog`):
  `dockerd.log`, `cert-injector.log`, `proxy.log`.
- Host-side (`<workspace>/.corral/logs/`): `mitm.log` (mitmweb output, proxy
  mode). `proxy.log` is also visible here since that directory is the mount.
- Suite artifacts (`tests/e2e/.artifacts/`): captured `go build` / `init` /
  `start` stdout+stderr, the resolved `config.json`, the inner build log, and (on
  a failed boot) the outer container logs.

## Port-bridge mechanism

`corral start` publishes the outer container's `3000:3000` to the host. The
inner DinD container publishes onto the DinD bridge gateway `172.18.0.1:3000`.
Nothing forwards between the two automatically, so the harness installs a `socat`
bridge inside the outer container: `0.0.0.0:3000 → 172.18.0.1:3000`
(`bridgePublishedPort` in `lib/corral.ts`). This runs inside the Linux
container on both macOS and Linux, so the host assertion is platform-identical.
```
