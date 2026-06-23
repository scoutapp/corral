# Transparent egress for sandclaude — drop `http_proxy` everywhere

## IMPLEMENTATION STATUS (as built — read this first)

Final design after implementation + live testing in `sandclaude dev`:

| Traffic source | Mechanism | Status |
|---|---|---|
| **Inner DinD containers** | PREROUTING REDIRECT (172.16/12 → transparent listener :3129); no proxy env, no daemon.json proxies block | ✅ **WORKING end-to-end** — allowlisted `CODE=401` in 0.26s, blocked enforced, full verified-TLS (mitm CA) works |
| **Outer container procs (Claude)** | Explicit `HTTP_PROXY=127.0.0.1:3128` env (unchanged) | ✅ **Works** (verified `HTTP 401` through proxy+mitm) |
| **dockerd daemon (image pulls)** | `HTTP_PROXY` in dockerd's *process* env (not injected into containers) | ✅ **Works** (`docker pull` succeeds) |

**Inner-container transparent capture is fully working.** Verified from a clean `sandclaude dev` build: an inner container with NO proxy env vars and its own DNS resolves, connects, and reaches allowlisted hosts transparently (`CODE=401` in ~0.26s); blocked hosts are rejected (`BLOCKED … (transparent)`); and with the mitm CA trusted, full verified TLS interception works.

**Why outer is NOT transparent:** OUTPUT-chain interception of locally-generated traffic does not work in Docker Desktop's LinuxKit VM. `nat OUTPUT REDIRECT` to 127.0.0.1 is not delivered to the listener even with `route_localnet=1`/`rp_filter=0`; `DNAT`-to-container-IP delivers but the reply 5-tuple is broken; TPROXY-for-local (mangle OUTPUT MARK → policy route to lo → mangle PREROUTING TPROXY) marks+routes packets (counters climb) but never completes. eBPF `connect()` could do it but is deferred. So outer keeps the single explicit env var (set once, not "everywhere").

**The transparent listener** (`allowlist-proxy/transparent.go`): recovers original dst via `SO_ORIGINAL_DST` (REDIRECT) or `LocalAddr` (TPROXY, needs IP_TRANSPARENT + CAP_NET_ADMIN — degrades gracefully if absent), parses SNI / Host, allowlist-checks (reusing `Allowlist.Allowed`), tunnels via `dialTarget` (shared with explicit CONNECT handler; forces tcp4 upstream; `readHTTPStatusLine` avoids bufio over-read of tunnel bytes), then splices both directions with half-close. Listener binds **tcp4** (IPv6 dual-stack socket does not receive REDIRECTed IPv4 packets).

**The bug that took longest (now fixed):** `sniffHost` used `bufio.Peek(4096)` to read the SNI, but `bufio.Peek(n)` *blocks until n bytes arrive or EOF*. A ClientHello is ~1.5 KB, so it waited ~10 s for bytes that never came, stalling the whole tunnel until the client timed out. Fixed by reading the TLS record length from the 5-byte header and peeking exactly that record (HTTP path peeks incrementally to end-of-headers).

**Also required for inner containers:** a `nat POSTROUTING MASQUERADE` for `172.18.0.0/16 ! -d 172.16.0.0/12` so inner containers' own DNS (UDP 53, not caught by the TCP PREROUTING REDIRECT) reaches the resolver and returns. Containers no longer get `HTTP_PROXY` injected, so they resolve DNS themselves; `daemon.json` has `ip-masq=false`, hence the explicit rule.

**Remaining caveat — cert-injector + ephemeral containers:** inner containers now depend entirely on the `bin/cert-injector` (no proxy-env fallback) to trust the mitm CA. Its create→start→restart model is racy for very short-lived `--rm` one-shot containers (they exit before injection completes). Long-running service containers are fine. For ephemeral curls, mount `/etc/proxy-ca.crt` or use `-k`. This is a pre-existing cert-injector limitation, out of scope here, but more exposed now — a candidate follow-up is a more robust CA-trust mechanism (e.g. a read-only CA bind into every container, or a dockerd default mount).

**Testing gotchas discovered:** (1) `go build` must run from `allowlist-proxy/` (repo root has a different `main.go`); Dockerfile builds the package (`go build .`) not `main.go`. (2) `docker exec` does not inherit entrypoint's runtime `export`s — it sees the original `docker run -e` env, so test the outer path with `-x http://127.0.0.1:3128` explicitly. (3) Background `docker run` invocations spawn zombie containers that pile up and overwhelm the daemon — always `docker rm -f` between tests. (4) `iproute2` + `libcap2-bin` + `tcpdump` are baked into the image (TPROXY tooling + packet diagnostics; tcpdump on `docker0` was how the ~10 s SNI-peek stall was finally pinned down).

---

## Context

Today every program in the sandbox must be *told* about the proxy via `HTTP_PROXY`/`HTTPS_PROXY` env vars — set in Claude's shell (`entrypoint.sh:168-171`) and injected into every DinD inner container (`entrypoint.sh:234-289`). This is brittle: any tool that ignores those env vars (or a container started without them) either bypasses the guard or fails. The goal is **transparent capture**: the kernel silently reroutes all outbound TCP to the proxy, so nothing in the box needs proxy config at all.

**Scope (locked with user):**
- ✅ **Both** capture sites: DinD inner containers **and** the outer container's own processes (Claude's shell).
- ✅ Mechanism: **iptables `REDIRECT` + a transparent listener in the proxy. No eBPF** (not needed for this; deferred as possible future polish).
- ❌ **Out of scope:** the static-rootstore / Rust-webpki trust-store problem (a binary with a compiled-in root list can't trust the injected CA — unfixable from outside the process; see "Deferred" below). We keep the existing env-var CA trust + cert-injector as-is.

## What stays the same

- mitmproxy on host (`main.go:225-244`) and the `--upstream` chain (`entrypoint.sh:88-92`) — unchanged. **Always MITM** still holds for everything that *can* trust the CA.
- cert-injector (`bin/cert-injector`) and the CA trust env vars (`entrypoint.sh:31-34`) — unchanged.
- The DinD NAT REDIRECT already in place (`entrypoint.sh:311-315`) — reused, not replaced.

## The one new component: a transparent listener in the proxy

Verified gap (`allowlist-proxy/main.go:250-326`): the proxy is built on Go's `net/http` server. It reads the destination from `r.Host` (the proxy-protocol `CONNECT` line) then `Hijack()`s to tunnel bytes. **A REDIRECTed connection has no `CONNECT` line**, so `r.Host` is empty — the current handler cannot serve it. The transparent path must be a **separate raw `net.Listener` accept loop** (new), on a new dedicated port (e.g. `3129` "transparent", keeping `3128` as the existing explicit-proxy port for back-compat / the upstream chain).

Transparent flow per accepted connection:
1. Accept raw redirected TCP on the transparent port.
2. **`getsockopt(SO_ORIGINAL_DST)`** — recover the pre-REDIRECT destination `IP:port` from kernel conntrack (REDIRECT overwrote the socket's local addr). In Go: `syscall.GetsockoptIPv6Mreq(fd, IPPROTO_IP, SO_ORIGINAL_DST)` pattern on the raw conn's fd (via `SyscallConn().Control`).
3. Determine the domain: peek the TLS **ClientHello → SNI** for :443 / TLS; fall back to the `Host:` header for plain HTTP; fall back to the original-dst IP if neither (IP-only flows are then IP-gated — acceptable, see Open item).
4. Allowlist-check the domain — **reuse** `Allowlist.Allowed` (`main.go:135`).
5. Forward to mitmproxy — **reuse** the existing upstream CONNECT logic (`main.go:274-292`), issuing `CONNECT <domain-or-ip>:<port>` to `--upstream`, then splice bytes (same `io.Copy` tunnel as `main.go:316-325`).

This keeps the SNI/ClientHello bytes intact in the spliced stream so mitmproxy still terminates TLS normally.

## Capture rules (iptables) — both sites

**Site B — DinD inner containers:** already done (`entrypoint.sh:311-315`). Only change: point the REDIRECT at the new transparent port and drop the now-redundant env injection (below). The existing rule:
```bash
iptables -t nat -A PREROUTING -s 172.16.0.0/12 -p tcp ! -d 172.16.0.0/12 -j REDIRECT --to-port <transparent-port>
```

**Site A — outer container's own processes:** add a `nat OUTPUT` REDIRECT, **excluding `proxyuser`** (UID 900, `Dockerfile:111`) so the proxy's own upstream connections to mitmproxy don't loop back into the transparent listener. Also exclude loopback and the DinD subnet. New rules (in `entrypoint.sh`, alongside the existing OUTPUT block ~`192-199`):
```bash
# Do NOT redirect the proxy's own egress, loopback, or inter-container traffic
iptables -t nat -A OUTPUT -m owner --uid-owner proxyuser -j RETURN
iptables -t nat -A OUTPUT -o lo -j RETURN
iptables -t nat -A OUTPUT -p tcp -d 172.16.0.0/12 -j RETURN
# Redirect everything else (all non-proxy TCP) to the transparent proxy
iptables -t nat -A OUTPUT -p tcp -j REDIRECT --to-port <transparent-port>
```
The existing **filter OUTPUT lockdown stays** (`entrypoint.sh:193-199`): `--uid-owner proxyuser` ACCEPT + default `REJECT`. Defense in depth — even if a packet dodged the REDIRECT, it still can't leave except as `proxyuser`. The REDIRECT happens in `nat OUTPUT` (before `filter OUTPUT`), so redirected packets become loopback-destined and pass the filter via the `-o lo` ACCEPT.

> **Critical safety property:** `proxyuser` is excluded from REDIRECT (so no loop) but is the only UID allowed direct egress by the filter rule (so the lockdown holds). This is the load-bearing invariant — verify it explicitly.

## Remove the now-redundant env injection

Once transparent capture works, delete:
- Claude shell exports `entrypoint.sh:168-171` (`HTTP_PROXY/HTTPS_PROXY/http_proxy/https_proxy`).
- Inner dockerd `daemon.json` `proxies` block `entrypoint.sh:234-238` and `~/.docker/config.json` `proxies` `entrypoint.sh:287-292`.

**Keep** (these are NOT app-facing proxy config — they're the proxy chain itself / build args):
- `--upstream $HTTP_PROXY` (`entrypoint.sh:88-92`) — the mitmproxy chain.
- `main.go:611-612` host-side mitmproxy env, `main.go:743-748` `--build-arg` (build-time; `docker build` doesn't get REDIRECTed the same way — leave unless proven redundant).

## Critical files

- `allowlist-proxy/main.go` — **add transparent listener** (new accept loop + `SO_ORIGINAL_DST` + SNI peek); reuse `Allowlist.Allowed` and upstream logic.
- `entrypoint.sh` — add `nat OUTPUT` REDIRECT (Site A), retarget PREROUTING port (Site B), remove redundant env exports + daemon proxy blocks, start the transparent listener.
- `Dockerfile` — `proxyuser` UID 900 already exists (`:111`); no change unless the listener needs a new port opened.
- `main.go` — only if the transparent port needs plumbing as a flag/env into the container.

## Verification (in `sandclaude dev`)

1. **Outer (Site A):** in Claude's shell run `unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; curl -v https://<allowlisted>` → succeeds, `ALLOWED` in `~/logs/proxy.log`; `curl https://<blocked>` → blocked. With the env vars gone entirely, the same must hold.
2. **Inner (Site B):** `docker run --rm -e HTTP_PROXY= -e HTTPS_PROXY= curlimages/curl curl -v https://<allowlisted>` → succeeds + `ALLOWED`; blocked host → blocked.
3. **No loop / lockdown intact:** confirm `~/logs/proxy.log` shows no proxy-self connections; `sudo iptables -t nat -L OUTPUT -n -v` and `sudo iptables -L OUTPUT -n -v` show climbing hit counts and `proxyuser` RETURN before the REDIRECT.
4. **MITM still works:** an allowlisted HTTPS host with credential injection (e.g. `api.github.com`) still gets creds injected → proves the SNI/ClientHello survived the transparent splice into mitmproxy.

## Deferred (explicitly not in this change)

- **Static-rootstore trust (Rust/webpki-roots):** such binaries ignore the system store and all `*_CA*` env vars; the injected CA can't reach them, and **no syscall/eBPF hook can force trust** (cert validation is userspace, post-`connect()`). The only universal fix is per-host **TLS passthrough** (forward encrypted, gate on SNI, no decrypt → no injection) — mitmproxy's `tls_passthrough`/`--ignore-hosts`, currently NOT wired in. Revisit if/when a real Rust binary needs it.
- **eBPF `connect()` cgroup redirect:** cleaner than NAT, but unnecessary here and unverified inside Docker Desktop's LinuxKit VM (macOS). Possible future replacement for the iptables REDIRECT.
- **SSH-to-GitHub credential injection:** different mechanism entirely (HTTP header injection can't touch SSH); use a git `insteadOf` HTTPS rewrite + existing `GH_TOKEN` instead.

## Open item to resolve during implementation

IP-only / non-SNI TCP (no `Host`, no ClientHello — e.g. raw TCP, some DB clients) can only be **IP-gated**, not domain-gated, under transparent capture. Default: gate by original-dst IP; if that's too weak, fail-closed those flows. Decide when first encountered — does not block the main change.
