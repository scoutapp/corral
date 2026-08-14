-- Automations — the second tenant of the Corral database.
--
-- Turns hard-coded event bodies (PR approve/comment/merge, "Analyze with AI",
-- project start, …) into user-configurable units of work. This is the data
-- layer for a general action + trigger + run engine; the dashboard is the first
-- consumer, with a CLI/OpenAPI control plane and composed "flows" to follow.
--
-- Design notes carried into the schema:
--   * Actions are provider-agnostic CAPABILITIES (pr-approve, pr-comment, …);
--     the concrete driver (GitHub via gh) lives in Go, not here. spec_json holds
--     per-kind typed config.
--   * hooks bind an event to an action|flow, ordered, with global + per-repo
--     scope (repo_id NULL = global default; a repo row overrides).
--   * flow_steps are ORDERED today but GRAPH-READY: each step has a stable
--     step_key and a depends_on_json list, so DAG execution is additive later
--     rather than a rewrite.
--   * runs is append-only execution history (audit, debugging, future macro
--     replay): every action/flow execution writes one row.
--   * repo_id is Corral's existing TEXT Repo.ID (sha of URL), matching the pr_
--     tables — not a FK into a table we own here.

-- Reusable units of work.
CREATE TABLE auto_actions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL,           -- capability | bash | claude_prompt | webhook | slack
    spec_json  TEXT    NOT NULL DEFAULT '{}',  -- typed config per kind
    scope      TEXT    NOT NULL DEFAULT 'global',  -- global | repo
    repo_id    TEXT,                        -- NULL for global; Corral Repo.ID for repo-scoped
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_auto_actions_scope ON auto_actions (scope, repo_id);
CREATE INDEX idx_auto_actions_kind  ON auto_actions (kind);

-- Composed units of work (linear today, DAG-ready).
CREATE TABLE auto_flows (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    scope      TEXT    NOT NULL DEFAULT 'global',
    repo_id    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_auto_flows_scope ON auto_flows (scope, repo_id);

-- Steps within a flow. Ordered by position; graph-ready via step_key +
-- depends_on_json (unused while execution is linear).
CREATE TABLE auto_flow_steps (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    flow_id         INTEGER NOT NULL REFERENCES auto_flows (id) ON DELETE CASCADE,
    position        INTEGER NOT NULL,
    action_id       INTEGER NOT NULL REFERENCES auto_actions (id) ON DELETE CASCADE,
    step_key        TEXT    NOT NULL,       -- stable handle: {{steps.<step_key>.output}}
    depends_on_json TEXT    NOT NULL DEFAULT '[]',   -- [] linear; populated for DAG later
    input_map_json  TEXT    NOT NULL DEFAULT '{}'    -- context/prior-output → action inputs
);
CREATE INDEX idx_auto_flow_steps_flow ON auto_flow_steps (flow_id);

-- Event → action|flow bindings. Multiple hooks per event allowed, ordered by
-- position. A repo-scoped hook overrides/augments the global default.
CREATE TABLE auto_hooks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event       TEXT    NOT NULL,           -- pr.approve | pr.comment | pr.request_changes
                                            -- | pr.merge | pr.analyze | pr.enter | project.start
    scope       TEXT    NOT NULL DEFAULT 'global',
    repo_id     TEXT,
    target_kind TEXT    NOT NULL,           -- action | flow
    target_id   INTEGER NOT NULL,           -- row in auto_actions | auto_flows
    position    INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_auto_hooks_event ON auto_hooks (event, scope, repo_id);

-- Append-only execution history. One row per action/flow run.
CREATE TABLE auto_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger      TEXT    NOT NULL,          -- hook | manual | api | flow-step
    event        TEXT,                       -- the event, when trigger=hook
    target_kind  TEXT    NOT NULL,           -- action | flow
    target_id    INTEGER NOT NULL,
    status       TEXT    NOT NULL,           -- running | ok | error | partial
    context_json TEXT    NOT NULL DEFAULT '{}',  -- input bag (pr number, repo, url, actor…)
    steps_json   TEXT    NOT NULL DEFAULT '[]',  -- per-step: cmd, exit, stdout, stderr, duration
    started_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    finished_at  TEXT
);
CREATE INDEX idx_auto_runs_target ON auto_runs (target_kind, target_id);
CREATE INDEX idx_auto_runs_started ON auto_runs (started_at);
