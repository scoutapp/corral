-- Application logs — one host-wide, searchable record of everything the app
-- does or runs (AI analysis, PR actions, project lifecycle, automation runs,
-- scripts, HTTP requests). The generalized cousin of auto_runs: auto_runs keeps
-- the detailed step-by-step record of an automation execution; app_logs is the
-- flat, queryable stream the Logs tab reads. An automation run writes ONE
-- summary row here carrying its run_id, so the Logs tab can deep-link to the
-- run's detail without duplicating step data.

CREATE TABLE app_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    level       TEXT    NOT NULL,               -- debug | info | warn | error
    category    TEXT    NOT NULL,               -- ai | pr-action | project | automation | script | repo | chat | http | system
    event       TEXT    NOT NULL,               -- ai.analyze, pr.merge, project.start, automation.run, http.request, …
    message     TEXT    NOT NULL,               -- human one-liner
    repo_id     TEXT,                           -- Corral Repo.ID when scoped
    project_id  TEXT,                           -- workspace/project id when scoped
    status      TEXT,                           -- ok | error | partial (null for pure info)
    duration_ms INTEGER,                        -- for timed operations
    meta_json   TEXT    NOT NULL DEFAULT '{}',  -- structured detail (pr number, action, error text, …)
    run_id      INTEGER                         -- links to auto_runs when this log IS an automation run
);

-- Keyset paging cursor (newest-first) + common filter columns.
CREATE INDEX idx_app_logs_id       ON app_logs (id DESC);
CREATE INDEX idx_app_logs_cat      ON app_logs (category, id DESC);
CREATE INDEX idx_app_logs_project  ON app_logs (project_id, id DESC);
CREATE INDEX idx_app_logs_repo     ON app_logs (repo_id, id DESC);
CREATE INDEX idx_app_logs_ts       ON app_logs (ts);   -- retention pruning by age

-- Free-text search uses a LIKE scan over message + meta_json rather than FTS5.
-- Rationale: FTS5 requires the sqlite_fts5 build tag across every corral build
-- (dev + goreleaser), a cross-cutting change; and the log is retention-capped
-- (default 100k rows), so a LIKE scan over the (small) result window is fast
-- enough. No extra module/build-tag dependency. Can be upgraded to FTS5 later
-- behind a build tag if volume ever warrants it.
