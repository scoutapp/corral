-- Pseudo-cron for flows. NOT real cron: the machine (a laptop) is often off, so
-- a background tick reconciles each schedule's last_run_at against its cadence
-- rather than firing at wall-clock instants. If the machine was off past several
-- intervals, a schedule fires ONCE on wake (catch_up=1), never once per missed
-- interval — drift-tolerant by design. catch_up=0 means "skip the miss, wait for
-- the next interval boundary."

CREATE TABLE auto_schedules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    flow_id         INTEGER NOT NULL REFERENCES auto_flows (id) ON DELETE CASCADE,
    cadence_seconds INTEGER NOT NULL,               -- interval between runs (e.g. 86400 = daily)
    last_run_at     TEXT,                           -- UTC ISO; NULL = never run (fires on first due tick)
    catch_up        INTEGER NOT NULL DEFAULT 1,     -- 1 = fire once on wake if overdue; 0 = skip the miss
    enabled         INTEGER NOT NULL DEFAULT 1,     -- 0 = paused, not fired
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- One schedule per flow keeps the model simple (a flow is either scheduled or
-- not); revisit if multiple cadences per flow are ever needed.
CREATE UNIQUE INDEX idx_auto_schedules_flow ON auto_schedules (flow_id);

-- The tick scans enabled schedules; keep that cheap.
CREATE INDEX idx_auto_schedules_enabled ON auto_schedules (enabled);
