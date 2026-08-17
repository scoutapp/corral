-- Small key/value store for dashboard settings that need to live in the DB (as
-- opposed to ~/.corral/global-settings.json, which holds host/config-file
-- settings). The first user is the global chat's capability, which is
-- deliberately NULL until the user chooses on first run — the ABSENCE of a row is
-- the "not configured yet" signal that triggers the first-run prompt. A JSON file
-- can't cleanly represent "unset vs chosen-read-only", but a missing row can.

CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
