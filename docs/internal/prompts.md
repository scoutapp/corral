# Prompts

A prompt is the first thing Claude reads when a sandbox starts. Corral has two
kinds: the **project-start prompt** (sent automatically) and your **saved prompts**
(a library you pick from). Both live at **Automations → Automations**.

![Prompts on the Automations page](img/automations.png)

## The project-start prompt

Typed into Claude when a plain sandbox launches (New project, or Verify-in-sandbox
without a preset). It's a template with placeholders corral fills in:

```
You're working in a sandboxed checkout of {{repo}} on branch {{branch}}.
Explore the codebase, then help with the task at hand. {{ssh_guidance}}
```

Available placeholders: `{{repo}}`, `{{branch}}`, `{{ssh_guidance}}` (filled only
when an SSH key is loaded). Edit it and **Save**, or **Reset to default**.

## Saved prompts (the library)

Reusable prompts you keep around and pick at launch — e.g.:

```
fix-flaky-tests → Find and fix flaky tests in {{repo}}. Run the suite, identify
                  non-deterministic failures, and make them reliable.
```

**+ New prompt** to add one. When you start a project or verify a PR, a picker lets
you choose a saved prompt instead of the default. Placeholders work the same way.

## Scope: global vs. repo

- A prompt marked **global** applies everywhere.
- A repo's **Settings → Automations** can override or add prompts just for that repo.

Repo beats global when both exist.

## Build with AI

Not sure what to write? **Build with AI** on the project-start prompt hands the job
to the host Claude, which drafts a template for you. (Host-only, read-only — it's
not running in a sandbox.)

## Gotchas

- Placeholders are literal `{{name}}`. An unknown one just renders empty.
- Editing the project-start prompt changes it for *every* plain launch — use a
  saved prompt if you want a one-off.
