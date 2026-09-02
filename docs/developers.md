# Developing Corral

How to build Corral from source, run its tests, and actually iterate on it. If you
just want to *use* Corral, see the [usage guide](usage.md) instead. For the design,
see [architecture.md](architecture.md).

## Build from source

```bash
git clone https://github.com/scoutapp/corral.git && cd corral

# Build + install into your PATH (also syncs the runtime asset bundle to ~/.corral/assets).
./install.sh

# Or just build a binary in the checkout and run it in place:
go build -tags sqlite_fts5 -o corral ./cmd/corral
./corral list
```

The `sqlite_fts5` build tag is required — the activity log and conversation capture
use SQLite full-text search. `./install.sh` passes it for you; pass it yourself when
you `go build` directly.

## The dashboard frontend

The browser UI lives in `internal/dashboard/webui/app-src` (React + Vite). The built
output is **committed** to `internal/dashboard/webui/static/app` and embedded into the
Go binary with `go:embed`. So a change to the UI is a two-step build:

```bash
cd internal/dashboard/webui/app-src
npm install            # first time only
npm run typecheck      # tsc, catches type errors
npm run build          # writes the committed bundle in ../static/app

cd -                   # back to the repo root
go build -tags sqlite_fts5 -o corral ./cmd/corral   # embed the new bundle
```

Commit the regenerated `static/app` bundle alongside your source change — CI and the
release build use the committed bundle, they don't rebuild the frontend.

## Tests

```bash
go test ./...                       # Go unit/integration tests
(cd tests/e2e && npm ci && npm test)  # Playwright end-to-end suite (also runs in CI)
```

Only the e2e suite runs in CI (`.github/workflows/e2e.yml`) — it installs Corral the
real way and drives a live sandbox. The Go tests are run locally.

> **Gotcha:** `go test ./internal/dashboard/` can be slow; if it hangs, it's almost
> always a single heavy test, not your change. Narrow with `-run` while iterating.

## Releasing

Releases are cut by pushing a semver tag:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

That triggers `.github/workflows/release.yml`, which runs GoReleaser
(`.goreleaser.darwin.yml` + `.goreleaser.linux.yml`) to build the cross-platform
binaries and the `corral-assets.tar.gz` bundle, then publishes one GitHub Release with
all archives + `checksums.txt`. The installer downloads those. Match the existing
annotated-tag changelog format (see `git tag -n20 <recent-tag>`).

## Closing the loop: iterate with a local Claude, not Corral-on-Corral

The obvious-looking move — point Corral at the Corral repo and let a sandboxed Claude
develop Corral — is the one to avoid. It fights the very thing Corral does:

- **The MITM credential injection gets in the way.** Every request a sandbox makes is
  routed through Corral's proxy, which swaps in your real credentials. When the code
  *under development* is itself that proxy (and the credential plumbing, the firewall,
  the cert injection), a sandboxed run is testing two moving copies at once — the one
  you're editing and the one wrapping it. Failures become ambiguous: is the bug in your
  change, or in the outer Corral's interception of it? You lose the clean signal that
  makes a dev loop fast.
- **A mocked Claude doesn't buy much.** You *can* stub the model to avoid real API
  calls, but a mock that returns canned responses can't exercise the real agent
  behavior you're usually trying to change, so it rarely tells you what you need to know.

**Do this instead:** develop Corral with a **local Claude Code instance running directly
against the checkout** — no sandbox, no proxy in the middle. Edit, `go build`, run the
binary, observe. Use Corral's own sandbox only to *verify* a finished change behaves
correctly end-to-end, the same way any user would — not as your inner-loop editor.

In short: Corral is the thing that wraps your agent. Don't wrap the thing you're editing
in a copy of itself. Keep the loop local and flat.
