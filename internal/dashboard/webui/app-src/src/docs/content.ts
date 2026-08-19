// Per-page docs content for the left-side docs drawer.
//
// Written for HUMANS: brief, task-oriented, "how do I…" with small examples —
// not exhaustive reference. Each page maps to one DocPage. Keep entries short;
// if something needs a wall of text it probably belongs in the code comments,
// not here.

export interface DocBlock {
  // A short paragraph. Inline `code` spans are rendered (single backticks).
  p?: string;
  // A bullet list. Each item may contain inline `code`.
  list?: string[];
  // A fenced example (rendered monospace). Keep it a few lines at most.
  code?: string;
  // A small heading within the page.
  h?: string;
}

export interface DocPage {
  title: string;
  blocks: DocBlock[];
  // Optional pointer to the in-repo deep-dive guide (path under docs/). Shown as
  // a "Full guide" footer so the drawer stays short but says where more lives.
  more?: string;
}

// docsFor returns the doc page for a route path, falling back to the landing doc.
export function docsFor(path: string): DocPage {
  if (/^\/p\//.test(path)) return PROJECT;
  if (/^\/repos\/[^/]+\/prs\/\d+/.test(path)) return PR_REVIEW;
  if (/^\/repos\//.test(path)) return REPO;
  if (path === "/global") return GLOBAL;
  if (path === "/logs" || path === "/automations/logs") return LOGS;
  if (path === "/integrations") return INTEGRATIONS;
  if (path === "/automations/runs") return RUN_LOG;
  if (path === "/automations") return AUTOMATIONS;
  return PROJECTS;
}

const PROJECTS: DocPage = {
  title: "Projects",
  blocks: [
    { p: "Every project is a repo checkout running Claude Code in an isolated, network-firewalled Docker sandbox. This is the home screen — one pane per project." },
    { h: "Start a project" },
    { p: "Hit **New project** and pick how to seed it:" },
    {
      list: [
        "**From repo(s)** — clone one or more repos into a fresh sandbox.",
        "**Blank dir** — an empty workspace.",
        "**Existing dir** — point at a folder you already have.",
      ],
    },
    { p: "It starts automatically. Click a pane to open the project; the ✕ on a pane stops it." },
    { h: "What a pane shows" },
    { p: "Live status per project — whether the container and Claude are up, recent activity, and a peek at the last terminal line. It polls, so it stays fresh on its own." },
    { p: "Credentials never enter the sandbox — the host proxy injects them. So a project can use `gh`/`git` without a real token ever being inside the container." },
  ],
  more: "docs/README.md",
};

const PROJECT: DocPage = {
  title: "Project",
  blocks: [
    { p: "One sandboxed project. The right dock is Claude; the tabs on the left are how you inspect and drive the box." },
    { h: "Tabs" },
    {
      list: [
        "**Files** — browse and edit the workspace.",
        "**Diff** — the git diff of what Claude changed.",
        "**Container** — a shell inside the sandbox (`docker exec`).",
        "**Live View** — watch a web app the sandbox is running (see below).",
        "**Mitm Proxy** — inspect the HTTPS traffic the sandbox made.",
        "**Firewall Log** — what the allowlist allowed/blocked.",
        "**Config** — network, DinD, ports, SSH keys, credentials.",
      ],
    },
    { h: "Talk to Claude" },
    { p: "The dock on the right is this project's Claude. Type to it; it drives the sandbox. The host shell below it is a plain shell on your machine (not sandboxed)." },
    { h: "Live View" },
    { p: "Watch a web app running in the sandbox, embedded here. Pick a detected port or type one, and it loads." },
    { p: "Running the app inside Docker-in-Docker? Publish its port so it's reachable:" },
    { code: "docker run -d -p 3000:3000 myapp" },
    { p: "A bare `EXPOSE` (no `-p`) won't show up. An app in the outer container bound to `0.0.0.0` works without `-p`." },
    { h: "Change how it runs" },
    { p: "**Config** holds the restart-required bits: network protection, Docker-in-Docker + published ports, a DinD **data cache** to start from, SSH keys, and per-host credentials. Edits there prompt you to restart the project." },
  ],
  more: "docs/live-view.md",
};

const REPO: DocPage = {
  title: "Repo",
  blocks: [
    { p: "A repo corral tracks. Spawn sandboxes from it, review its PRs, and attach context that follows every checkout." },
    { h: "Tabs" },
    {
      list: [
        "**PR Review** — corral's read on the repo's open PRs.",
        "**Issues** — issues you can spin a sandbox off of.",
        "**Projects** — sandboxes created from this repo.",
        "**Forensics** — per-file history/churn.",
        "**Settings** — skills & context, automations.",
      ],
    },
    { h: "Skills & context (Settings)" },
    { p: "Attach **skills** (reusable SKILL.md capabilities) and an **agent context** (a CLAUDE.md) to this repo. Every sandbox cloned from it carries them in automatically — so the right guidance and tools are always present." },
    { h: "Automations (Settings)" },
    { p: "Repo-scoped prompts, event hooks, and flows. Global ones apply everywhere; repo ones add on top." },
  ],
  more: "docs/skills-and-context.md",
};

const PR_REVIEW: DocPage = {
  title: "PR Review",
  blocks: [
    { p: "Corral's analysis of one PR — a fast read before you dive in." },
    { p: "Spin up a sandbox on the PR branch to verify a change hands-on, or comment/approve straight from here. Actions run as host operations against GitHub (never from inside the sandbox)." },
    { p: "Ask the Claude dock about the PR — it knows which PR you're looking at." },
    { h: "Merging" },
    { p: "The Merge button is a split-button: the ▾ picks how the merge runs." },
    {
      list: [
        "**Merge with host** (default) — rebase-and-merge on your host for speed. Opens a live Claude drawer that rebases onto the base branch, resolves conflicts, and merges. **Not sandboxed** — it runs your host Claude with Bash against a real checkout.",
        "**Merge with sandbox** — the same, but in a one-shot sandbox on the PR branch. Slower to start, but isolated; the sandbox tears itself down once the PR lands (toggle off in Global settings).",
        "**Merge** — a plain `gh pr merge`, no rebase. Fails if GitHub says the PR isn't mergeable.",
      ],
    },
    { p: "The **strategy** (squash / merge commit / rebase) is separate from the mode. It resolves per-repo → global → ask: the first time you merge a repo with nothing set, a modal asks and remembers your choice for that repo. Only methods your GitHub repo actually allows are offered." },
    { p: "Set the default mode + strategy in **Global settings → PR merging**. The rebase-and-merge procedure Claude follows is the editable **pr.merge** prompt in **Automations → Prompts**." },
  ],
  more: "docs/README.md",
};

const GLOBAL: DocPage = {
  title: "Global settings",
  blocks: [
    { p: "Host-wide settings and defaults that apply across all projects." },
    {
      list: [
        "Default SSH keys loaded into every sandbox's scoped agent.",
        "Global automations (prompts, hooks, flows) that apply everywhere.",
        "**PR merging** — the default merge mode (sandbox / host / plain), the default strategy, and whether a merge sandbox auto-tears-down.",
        "The dashboard's own preferences.",
      ],
    },
    { p: "Anything set per-repo or per-project layers on top of these." },
  ],
  more: "docs/README.md",
};

const AUTOMATIONS: DocPage = {
  title: "Automations",
  blocks: [
    { p: "Make corral do things on its own — react to events, run multi-step flows, reuse scripts." },
    { h: "Sub-tabs" },
    {
      list: [
        "**Automations** — prompts + trigger cards. Bind an event (PR opened, comment, project start…) to an action.",
        "**Flows** — compose multiple steps into a flow, then run or schedule it.",
        "**Scripts** — your saved bash-script library, callable from flows.",
      ],
    },
    { h: "A quick flow" },
    { p: "Add a flow, drop in steps (a prompt, a script, an MCP call), and give it a schedule or run it by hand. See runs in the **Run Log**." },
  ],
  more: "docs/automations.md",
};

const RUN_LOG: DocPage = {
  title: "Run Log",
  blocks: [
    { p: "History of automation + flow runs. Click a run to see its steps, timing, and output." },
    { p: "Use it to confirm a scheduled flow fired and to debug a step that failed." },
  ],
  more: "docs/logs.md",
};

const LOGS: DocPage = {
  title: "Logs",
  blocks: [
    { p: "A searchable, host-wide activity log across every project and the dashboard itself." },
    { p: "Filter by project or category, or search the text. Handy for “what happened around the time X broke?” — spans link related events together." },
  ],
  more: "docs/logs.md",
};

const INTEGRATIONS: DocPage = {
  title: "Integrations",
  blocks: [
    { p: "Connect MCP servers on the **host**. Once connected, the dashboard chat can use them." },
    { p: "These live in your host `claude mcp` registry — corral just drives it. Host-only by design: the sandbox never reaches these, keeping the trust one-directional." },
    { h: "Connect one" },
    { p: "Add the server, complete any auth it needs, and it shows as **connected**. Remove it to stop the host Claude from using it." },
  ],
  more: "docs/integrations.md",
};
