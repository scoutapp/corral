-- Local, internal PR notes: private annotations stored only in Corral's DB and
-- NEVER sent to GitHub (unlike pr comments, which post upstream). Cascades from
-- prs so a pruned/removed PR drops its notes too.
CREATE TABLE pr_notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id      INTEGER NOT NULL REFERENCES prs (id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    author     TEXT,                              -- optional label ("cli", a name); free text
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_pr_notes_pr ON pr_notes (pr_id);
