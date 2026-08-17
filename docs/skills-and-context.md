# Skills & context

Attach **skills** and a **CLAUDE.md context** to a repo, and every sandbox cloned
from it carries them in automatically. Find it at a repo's **Settings → Skills &
context**.

## Skills

A skill is a `SKILL.md` — a reusable capability (a workflow, a checklist, a set of
commands) Claude can use. Add one here and it's dropped into every checkout of this
repo at `.corral/skills/<name>/SKILL.md`, where Claude picks it up.

```
review-rules   → SKILL.md describing how this team reviews code
```

## Agent context (CLAUDE.md)

Free-form guidance that becomes the repo's `CLAUDE.md` at the workspace root — the
file Claude reads on boot. Use it for "always do X in this repo" instructions.

If the repo already commits its own `CLAUDE.md`, corral's context is appended below
a marker rather than clobbering it.

## Why bother

Set it once on the repo instead of re-explaining context every time you spin up a
sandbox. New projects start already knowing your conventions and tools.

## Gotchas

- Skill names must be filesystem-safe (letters, digits, `-`, `_`).
- This is **per-repo**. For host-wide defaults, use global settings.
