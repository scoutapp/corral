# nest-scout — transparent egress test fixture

Verifies that an **unmodified** app (stock Dockerfile, no proxy/CA config)
builds and runs inside DinD with full MITM decryption, reaching the external
web with zero changes.

## Run (from inside the sandclaude container)

```bash
cd /Users/jackrothrock/claude_sandbox/test-apps/nest-scout   # workspace is bind-mounted at the same path
docker build -t nest-scout .          # ~/bin/docker wrapper injects the mitm CA at build time
docker run --rm nest-scout            # app fetches https://scoutapm.com/... at runtime
```

## Expected output

```
[result] SUCCESS status=200 bytes=<n> title="Troubleshooting"
```

And in `~/logs/proxy.log` you should see (both tagged `(transparent)`):

```
ALLOWED  registry.npmjs.org (transparent)   # npm install during build
ALLOWED  scoutapm.com (transparent)         # runtime fetch
```

If `npm install` fails with `UNABLE_TO_VERIFY_LEAF_SIGNATURE`, the build did
not go through the `~/bin/docker` wrapper (check `which docker` →
`/home/claude/bin/docker`). `scoutapm.com` must be in the allowlist.
