-- Records when a repo was last analyzed (forensics + callgraph) and at which
-- commit, so the UI can detect "N new commits since analysis" and offer a
-- Re-analyze without auto-running it. One row per repo (keyed by Corral Repo.ID).
CREATE TABLE pr_repo_analysis (
    repo_id     TEXT PRIMARY KEY,
    head_sha    TEXT NOT NULL,          -- mirror HEAD at analysis time
    branch      TEXT,                   -- default branch analyzed
    analyzed_at TEXT NOT NULL DEFAULT (datetime('now')),
    cg_nodes    INTEGER NOT NULL DEFAULT 0,
    cg_edges    INTEGER NOT NULL DEFAULT 0
);
