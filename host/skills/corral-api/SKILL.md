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

## You are a CONDUCTOR — delegate tasks to worker Claudes

When you're the host chat driving Corral, act as a **conductor**, not a single
worker. For any real unit of work, **spawn a fresh worker Claude** to do it
instead of doing everything in this one session:

```
corral api POST /api/conductor/workers -d '{"title":"Triage flaky test","prompt":"..."}'
# → { "jobId": "worker-…" }
```

Each worker is a **fresh, independent Claude** (not a subagent) that runs the
prompt on the host in the background and gets its own tab in the dashboard's
**Work** panel, with a live working/idle indicator. You stay the conductor:
break the ask into tasks, kick off a worker per task, and keep going.

- **Do this at the START of any non-trivial request** — including every time you
  kick off work on a Corral project: create a worker for the task rather than
  running it inline.
- Put ALL context the worker needs in `prompt` (it starts fresh in a neutral dir).
- Give a short, human `title` — it's the worker's tab label.
- Workers, like merge jobs, are listed by `GET /merge-jobs`, streamed at
  `/merge-jobs/<id>/ws`, and removed with `DELETE /merge-jobs/<id>`.
- Workers run on the HOST and are **not sandboxed**; they use the operator's
  global-chat tool capability (read-only vs act).

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

## Conversation logs — debug what other Claudes did

Corral captures EVERY Claude conversation in the app — the global chat, project
"Ask Claude", PR-review chat, merge/worker jobs, one-shot analyses, the draft
flows, and the Claude running inside each sandbox — into a searchable store
(including tool calls). Use it to debug: "what did that worker actually run?",
"which conversation touched this file?", "trace how this project got started".

Over the API:

```
corral api GET /api/conversations                      # recent, newest-first
corral api GET "/api/conversations?origin=sandbox&project=<id>"
corral api GET "/api/conversations/search?q=flaky+test" # full-text across ALL messages/tools
corral api GET /api/conversations/<id>/messages         # one conversation's messages
corral api GET "/api/conversations/<id>/messages?q=grep" # search within one conversation
corral api GET /api/conversations/<id>/chain            # the causal chain it belongs to
```

The **chain** is the payoff: it follows `parent_conversation_id` up to the root
and gathers descendants, so from any conversation you can see the whole spawned
tree — e.g. a global chat → the worker/project it kicked off via this very API →
that project's own conversations. Combine with `GET /api/logs/trace/{traceId}`
(from a conversation's `traceId`) to see the timing waterfall.

There's also a CLI (reads the DB directly, no dashboard needed):

```
corral conversations --grep "flaky test"    # search
corral conversations show <id>              # print a conversation's messages
corral conversations chain <id>            # the causal chain
```

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

## Pruning old PR records (local cleanup)

Corral caches PRs it reviews in its LOCAL database. To clear out stale ones, use
the prune endpoint. It deletes **local records only** — it never touches GitHub
(nothing is closed or deleted upstream). Filtered on `fetched_at` (when Corral
last cached the PR):

```
corral api GET  /api/prs/prune?olderThanDays=30      # dry run — how many would go
corral api POST /api/prs/prune -d '{"olderThanDays":30}'   # actually prune
```

`olderThanDays` defaults to 30 and must be ≥ 1. Add `"repo":"<id>"` to scope it to
one repo. Prefer the GET dry-run first and tell the user the count before pruning.

## Merging a PR

```
corral api POST /api/prs/<prId>/merge -d '{"mode":"plain"}'   # direct gh merge
corral api POST /api/prs/<prId>/merge -d '{"mode":"host"}'    # background host job → {jobId}
```

- **mode `plain`** does a direct `gh pr merge` and fails if GitHub says the PR
  isn't mergeable (branch protection, required checks). It does NOT rebase.
- **mode `host`** starts a detached background job that rebases onto the base
  branch, resolves conflicts, waits for CI, and merges, then returns `{jobId}`.
  It runs the operator's HOST Claude — **not sandboxed**. The user watches it in
  the dashboard's Work tab.
- `mode` defaults to the user's configured default. `sandbox` mode is
  dashboard-only (it spins up a project); use `host` for a headless merge.

**The merge STRATEGY** (squash / merge / rebase) resolves per-repo → global. If
neither is set, the merge returns a 400 telling you to set one. **Set the
PER-REPO preference first** (it beats global and is the right default):

```
corral api GET /api/repos/<repoId>/merge-strategy
# → { allowed:[...], preferred:"", global_default:"", effective:"squash" }
corral api PUT /api/repos/<repoId>/merge-strategy -d '{"strategy":"squash"}'
```

Only offer a `strategy` in `allowed` (what GitHub permits for that repo). Prefer
setting the per-repo preference over a global default unless the user explicitly
wants it applied to every repo (global lives in the dashboard's Global settings).

## Running AI analysis on a PR

The AI analysis (per-block "Analyze with AI" + the PR risk verdict) runs host
Claude and takes a while, so the API is **fire-and-return**: start it, then poll.

```
corral api POST /api/prs/<prId>/enrich     # start per-block AI analysis (background)
corral api POST /api/prs/<prId>/analyze    # start the PR risk verdict (background)
corral api GET  /api/prs/<prId>/analysis   # poll → { enrich:{status}, risk:{status} }
```

`status` is `idle | running | done | failed`. Once `done`, read the results:

```
corral api GET /api/prs/<prId>/blocks      # analyzed blocks (after enrich)
corral api GET /api/prs/<prId>/risk        # risk verdict (after analyze)
```

Block ranking uses repo git-history forensics (not AI). Refresh it with:

```
corral api POST /api/repos/<repoId>/analyze          # git-only, synchronous
corral api GET  /api/repos/<repoId>/analysis-status  # analyzed / up-to-date?
```

## PR notes (private, local)

Notes are **local annotations stored only in Corral** — never posted to GitHub
(that's what a PR comment does). A scratchpad for the user/team:

```
corral api GET    /api/prs/<prId>/notes
corral api POST   /api/prs/<prId>/notes -d '{"body":"watch the migration in 0012"}'
corral api DELETE /api/prs/<prId>/notes/<noteId>
```

## Linking PRs (local relationships)

Relate two PRs in Corral's DB — shown on the PR page. These are **LOCAL** links
(tests / tested_by / related / depends_on + a note); nothing is pushed to GitHub.
`linkedPrId` and the ids below are Corral's INTERNAL PR ids (not GitHub numbers) —
get them from the list/suggest output.

```
corral api GET    /api/prs/<prId>/links            # existing links
corral api GET    /api/prs/<prId>/links/suggest    # candidates, ranked by file overlap
corral api POST   /api/prs/<prId>/links -d '{"linkedPrId":42,"relationship":"depends_on","note":"needs the schema change"}'
corral api DELETE /api/prs/<prId>/links/<linkId>
```

There's also a CLI: `corral pr links <prId>`, `corral pr suggest <prId>`,
`corral pr link <prId> <linkedPrId> [--rel depends_on] [--note "..."]`,
`corral pr unlink <prId> <linkId>`.

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
