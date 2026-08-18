---
name: corral-api
description: Drive the Corral dashboard from the host — list and act on repos, projects, GitHub issues, automations, flows, and the activity log through the `corral api` CLI. Use this when the user asks you to inspect or change Corral state (e.g. "what flows do I have", "start a project on that issue", "run the triage flow", "create an issue and start work on it"). HOST-ONLY: this talks to the loopback dashboard and never runs inside a sandbox.
---

# Corral API

Corral's dashboard exposes a host-side HTTP control plane. You drive it with one
command — `corral api` — which auto-discovers the running dashboard (its loopback
port + token from `~/.corral/dashboard.json`) and speaks to it. There are no
per-endpoint wrappers to memorize: **read the OpenAPI spec, then call the paths it
documents.**

> **Trust boundary.** This is a HOST capability. It talks to the operator's
> dashboard over loopback. It is never available inside a sandbox — a sandboxed
> Claude working in a project cannot and must not reach the dashboard. Only use
> this skill when you are the host chat assistant.

## Step 1 — discover the surface

Always start by reading the spec, so you use real paths and shapes rather than
guessing:

```
corral api GET /api/openapi.json
```

That returns an OpenAPI 3.1 document. Its `paths` are the exact routes you can
call; its `components.schemas` are the request/response shapes. The surface is
curated to the high-value operations (repos, projects, GitHub issues,
automations, flows, runs, prompts, logs/traces) — not every internal endpoint.

## Step 2 — call it

```
corral api <METHOD> <path> [-d '<json body>']
```

Examples:

```
corral api GET  /api/flows                              # list flows
corral api GET  /api/logs?category=ai                   # recent AI activity
corral api GET  /repos                                  # repos Corral knows
corral api POST /api/flows/3:run -d '{"vars":{"repo":"acme/widget"}}'
corral api POST /gh/issues/create -d '{"repo":"acme/widget","title":"Fix flaky test","body":"..."}'
corral api POST /p/<projectId>/start                    # start a project's container
```

- The response body (JSON) prints to stdout. Parse it and use it.
- A non-2xx response prints the error to stderr and exits non-zero — check for
  that and tell the user what failed.
- Bodies: `-d '<json>'` inline, or `-d @file.json` to read from a file.

## Reads vs writes — the permission gate

**Reads (GET) always work.** You can always inspect: list flows, read logs, look
at a PR, see project status.

**Writes (POST/PUT/DELETE) require the operator to have enabled API writes.** This
is off by default. If a write returns:

```
HTTP 403 ... API writes are disabled — enable them in the dashboard's global settings ...
```

then the user has not opted in. **Do not try to work around it.** Tell them
plainly: they can turn on **API access → Allow API writes** in the dashboard's
Global settings, then you can retry. This gate is deliberate — it's how the user
controls whether you can change things, not a bug.

## Creating skills & scripts — use the API, NOT files

When the user asks you to **create a skill or a tool/script for Corral**, create
it through the API. Corral stores skills and scripts in its **database**, not as
loose files — so **do not** write a `SKILL.md` or a `.sh` file on disk for this.
Writing a file is the wrong tool here: it won't be registered with Corral, and
the chat runs read-only by default so the write is denied anyway. Use these:

**A script/tool** — a reusable bash action (callable from flows + event hooks).
`spec` is a JSON *string* whose `script` is the shell body:

```
corral api POST /api/actions -d '{
  "name": "notify-slack",
  "kind": "bash",
  "scope": "global",
  "spec": "{\"script\":\"curl -sX POST $SLACK_URL -d \\\"text=$CORRAL_PR_TITLE\\\"\"}"
}'
```

Scope `global` applies everywhere; `repo` (with `"repoId":"<id>"`) scopes it to
one repo. Run-context values are exported to the script as `CORRAL_<UPPER_SNAKE>`.

**A skill** — a reusable `SKILL.md` capability injected into a repo's sandboxes.
Skills are **repo-scoped**; pass the repo id and the full markdown as `content`:

```
corral api POST /api/skills -d '{
  "repo": "<repoId>",
  "name": "review-rules",
  "content": "---\nname: review-rules\ndescription: How we review code here\n---\n\nWhen reviewing, always ..."
}'
```

Get `<repoId>` from `GET /repos`. The skill lands in the DB and Corral writes it
into every sandbox cloned from that repo (at `.corral/skills/<name>/SKILL.md`) —
you don't place the file yourself.

To edit or remove: `PUT /api/actions/<id>` / `PUT /api/skills/<id>`, or
`DELETE`. Re-read `GET /api/openapi.json` for the exact fields.

> These are writes, so the API-writes gate above applies — if it 403s, ask the
> user to enable API writes, then retry.

## Live View — point the user at the right port

The dashboard has a **Live View** tab that embeds a web app running in a project's
sandbox. When YOU start a web app in a project (a docs site, a dev server, an app
UI), tell the dashboard which port to show so the user doesn't have to hunt for it:

```
corral api PUT /p/<projectId>/live-port -d '{"port":1313}'
```

If the app serves its content under a **sub-path** (its root `/` 404s or is
blank — e.g. a docs site at `/docs/node/`), include the path so Live View opens
the right page:

```
corral api PUT /p/<projectId>/live-port -d '{"port":1313,"path":"/docs/node/"}'
```

Pick the **user-facing** app — the docs site, the UI, the thing they asked to see
— not a database, cache, or internal service (5432, 6379, …). Verify the path
actually returns 200 before setting it (curl it inside the sandbox). The Live
View tab then opens that port + path by default. Send `{"port":0}` to clear it.
For an inner (Docker-in-Docker) service to be viewable, run its container with
`-p <port>:<port>` so it's reachable.

## Is a project working or waiting?

`GET /status` returns each project's `activity`: `working` (a completion is
actively streaming), `waiting` (idle at the prompt, needs the user), or `off`
(container down). Use it to answer "is that project still going?":

```
corral api GET /status
# → { "projects": [ { "name": "...", "activity": "waiting", ... } ], ... }
```

This is a read — always allowed.

## Chaining work

A common request is a chain: pull something, create an issue, start work on it.
Do it as a sequence of `corral api` calls, checking each result before the next:

1. `GET` the data you need (e.g. `/api/logs` or an MCP result the user references).
2. `POST /gh/issues/create` — capture the returned issue `number` + `url`.
3. `POST /projects/create` (or `/p/<id>/start`) referencing that issue.

If any step 403s on the writes gate, stop and ask the user to enable API writes
rather than partially completing the chain.

## Tips

- When unsure of a path or body shape, re-read `GET /api/openapi.json` — it's the
  source of truth and is drift-checked against the real routes.
- `corral api` needs a running dashboard. If it reports no dashboard found, tell
  the user to run `corral dashboard`.
- Prefer the smallest set of calls that answers the request; don't enumerate the
  whole API when one endpoint will do.
