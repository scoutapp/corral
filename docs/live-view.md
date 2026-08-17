# Live View

Watch a web app your sandbox is running, embedded right in the dashboard. It's a
per-project tab.

## Use it

1. Run your app in the sandbox (or in its inner Docker).
2. Open the project's **Live View** tab.
3. Pick a detected port, or type one, and it loads.

The dashboard reaches into the container for you — no host ports to publish, no
tunnel to set up. The view is served under the dashboard's own auth.

## Inner-Docker apps need `-p`

If the app runs inside Docker-in-Docker, publish its port so it's reachable:

```bash
docker run -d -p 3000:3000 myapp
```

A bare `--expose` (no `-p`) won't show up. An app running directly in the outer
container, bound to `0.0.0.0` or `127.0.0.1`, works without `-p`. See
[DinD volumes](dind-volumes.md).

## Gotchas

- **Nothing listed?** The container may be stopped, or nothing's listening. Start
  the project / your app, then re-scan ports.
- The embedded app is **sandboxed** — it can't touch the dashboard. That's
  deliberate; it's untrusted content.
