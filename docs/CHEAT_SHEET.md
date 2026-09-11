# Talking to Claude — a cheat sheet

The global Claude in the dashboard (open it with **⌘K**, or the **Ask Claude**
button) is a *conductor*: you talk to it in plain English and it drives Corral for
you — starting projects, reviewing PRs, building images, running flows.

This page is a set of **things you can just say**, grouped by what you want to get
done. They're examples, not magic words — say them your own way. The repo names
here (`project-planner`, `invoice-service`, …) are made up; swap in yours.

> Tip: the more specific you are, the better. "Review PR #212 and post the
> findings as a comment" beats "look at that PR".

---

## Reviewing a pull request

Corral has two *different* things it can do to a PR. Knowing which you want gets
you the right result:

- **Full review** — Claude *reads* the PR (the diff, the risk analysis, the hot
  spots) and writes up a review. It never runs the code. Fast.
- **Sandbox verify** — Claude spins up a throwaway sandbox on the PR's branch and
  actually *runs* it: builds it, runs the tests, tries the app. Slower, but it
  proves the change works.

You can ask for either — or **both at once**.

| Say this | What happens |
|---|---|
| *"Do a full review of PR #212 in `project-planner`."* | Full review only. Reads + writes a review. |
| *"Review PR #212 and post the findings as a comment."* | Full review, then drafts a PR comment for you to approve. |
| *"Verify PR #212 works in a sandbox."* | Sandbox verify only. Runs the branch in a container. |
| *"Do a full review of PR #212 **and** verify it works in a sandbox."* | Both — the review runs on the host while the sandbox builds and tests. They run at the same time and report back separately. |
| *"What's the risk on PR #212?"* | Runs the risk analysis and summarizes it. |

---

## Starting work in a sandbox

Sandboxes are isolated containers where Claude does actual code work — editing,
building, running — without touching your real machine.

| Say this | What happens |
|---|---|
| *"Start a project on `invoice-service` and add PDF export to invoices."* | Creates a sandbox on the repo, hands Claude the task, and starts it. |
| *"Create a project using both `invoice-service` and `project-planner`, and wire the planner to read invoices from the invoice API."* | A single sandbox with **both** repos checked out, working the cross-repo task. |
| *"Spin up a sandbox on the `feature/webhooks` branch of `invoice-service` and get the tests passing."* | Same, but on a specific branch. |
| *"Work GitHub issue #88 in `project-planner`."* | Creates a sandbox seeded from the issue and starts on it. |

> "Create a project" and "start a project" go together — Claude creates it *and*
> starts it, then hands your task to the sandbox's own Claude and supervises.

---

## Getting an app running to look at

| Say this | What happens |
|---|---|
| *"Get `project-planner` running so I can see it."* | Boots the app in a sandbox and points the **Live View** tab at the right port. |
| *"The image for `invoice-service` is missing — build it."* | Builds the repo's Docker image inside a sandbox and saves it as the repo's baseline so future projects reuse it. |
| *"Verify the Live View actually renders, don't just assume."* | Drives a real browser at the Live View to confirm it's not a blank page. |

---

## Checking on things

| Say this | What happens |
|---|---|
| *"What projects are running right now?"* | Lists projects and whether each is working, waiting, or idle. |
| *"Is the `project-planner` sandbox stuck?"* | Reads that sandbox's conversation and tells you what it's doing. |
| *"Show me the open PRs across my repos."* | The cross-repo PR inbox. |
| *"What flows do I have, and when did the triage one last run?"* | Lists automations/flows and recent runs. |
| *"Pull up the recent AI activity."* | Recent entries from the activity log. |

---

## GitHub housekeeping

| Say this | What happens |
|---|---|
| *"Create an issue in `invoice-service`: 'Refunds double-charge on retry', and start work on it."* | Files the issue, then spins up a sandbox to work it. |
| *"Merge PR #212 in `project-planner` with squash."* | Merges via your configured strategy (waits for CI, no force). |
| *"Leave a note on PR #212: watch the migration in 0012."* | A private, local note — **not** posted to GitHub. |
| *"Comment on PR #212 with the review."* | Posts a comment to GitHub (this one *is* public). |

---

## Doing several at once

The conductor is happy to fan out. You can stack asks in one sentence:

- *"Do a full review of PR #212, verify it in a sandbox, and while that runs, get
  `project-planner` up in Live View."*
- *"Start projects on `invoice-service` and `project-planner`, and in each, run the
  test suite and tell me what fails."*

It kicks each off, keeps an eye on them, and reports back as results land.

---

### Two quick reminders

- **The global Claude runs on your machine, not in a sandbox.** It orchestrates;
  the *code work* happens inside sandboxes it creates. That's the safety boundary.
- **Nothing goes to GitHub unless you say so.** Reviews, notes, and analysis stay
  local until you explicitly ask to comment, merge, or push.
