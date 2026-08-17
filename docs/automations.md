# Automations & hooks

Run your own step when something happens — a Slack ping when you approve a PR, a
script when a project starts. Find it at **Automations → Automations**.

Each card is something corral already does (approve a PR, request changes, project
start…). You add steps to run **alongside** it.

## Add a step to an event

1. Find the event card, e.g. **When you approve a PR**.
2. **+ Add a step…**
3. Pick an action — a prompt, a script, or an MCP call.

Now that action fires every time the event happens.

## Events you can hook

PR approved, PR comment, request changes, PR merge, PR analyze, project start, and
more. Repo-scoped hooks live in a repo's **Settings**; global ones apply everywhere.

## Scripts get context

A script step runs sandboxed, with the event's data exported as env vars:

```bash
echo "PR $CORRAL_PR_NUMBER in $CORRAL_REPO_ID ($CORRAL_EVENT)"
```

## Bigger jobs → Flows

One step per event is a hook. Need several steps chained, or a schedule? That's a
[Flow](flows.md).

## Gotchas

- A hook runs on *every* occurrence of its event — keep it idempotent.
- Check the [Run Log](logs.md) to see whether a hook fired and what it did.
