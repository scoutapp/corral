-- PR Review — first tenant of the Corral database.
--
-- Ported 1:1 from the reference models. Adaptations for Corral:
--   * Tables are prefixed pr_ (they share corral.db with future Corral state).
--   * There is NO pr_repos table: repo identity is Corral's existing text
--     Repo.ID (sha of URL), and the repo registry stays in repos.json for now.
--     So repo_id columns are TEXT and are not FKs into a table we own here.
--   * PRs/blocks/etc. keep their own INTEGER autoincrement primary keys, which
--     the child rows reference via real foreign keys (FKs are enforced;
--     see store.go DSN _foreign_keys=on).

-- Per-file forensics (recomputed on demand), keyed by Corral Repo.ID.
CREATE TABLE pr_file_stats (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       TEXT    NOT NULL,
    file_path     TEXT    NOT NULL,
    total_commits INTEGER NOT NULL DEFAULT 0,
    fix_commits   INTEGER NOT NULL DEFAULT 0,
    churn_score   REAL,
    last_analyzed TEXT
);
CREATE INDEX idx_pr_file_stats_repo ON pr_file_stats (repo_id);
CREATE UNIQUE INDEX idx_pr_file_stats_repo_path ON pr_file_stats (repo_id, file_path);

-- Callgraph nodes (functions / methods / classes).
CREATE TABLE pr_cg_nodes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     TEXT    NOT NULL,
    file_path   TEXT    NOT NULL,
    symbol_name TEXT    NOT NULL,
    kind        TEXT    NOT NULL,          -- function | method | class
    line_start  INTEGER NOT NULL,
    line_end    INTEGER NOT NULL
);
CREATE INDEX idx_pr_cg_nodes_repo ON pr_cg_nodes (repo_id);

-- Callgraph edges (caller -> callee).
CREATE TABLE pr_cg_edges (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id   TEXT    NOT NULL,
    caller_id INTEGER NOT NULL REFERENCES pr_cg_nodes (id) ON DELETE CASCADE,
    callee_id INTEGER NOT NULL REFERENCES pr_cg_nodes (id) ON DELETE CASCADE
);
CREATE INDEX idx_pr_cg_edges_repo ON pr_cg_edges (repo_id);
CREATE INDEX idx_pr_cg_edges_callee ON pr_cg_edges (callee_id);

-- Pull requests.
CREATE TABLE prs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       TEXT    NOT NULL,
    pr_number     INTEGER NOT NULL,
    title         TEXT,
    short_summary TEXT,                    -- <=100 chars, LLM-generated
    github_url    TEXT,
    state         TEXT,
    base_sha      TEXT,
    head_sha      TEXT,
    raw_diff      TEXT,
    ai_analysis   TEXT,                    -- JSON blob (risk verdict)
    fetched_at    TEXT
);
CREATE INDEX idx_prs_repo ON prs (repo_id);
CREATE UNIQUE INDEX idx_prs_repo_number ON prs (repo_id, pr_number);

-- Logical code blocks within a PR (carousel items).
CREATE TABLE pr_blocks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id            INTEGER NOT NULL REFERENCES prs (id) ON DELETE CASCADE,
    order_index      INTEGER NOT NULL,
    priority         INTEGER NOT NULL DEFAULT 0,
    file_path        TEXT    NOT NULL,
    line_start       INTEGER NOT NULL,
    line_end         INTEGER NOT NULL,
    diff_hunk        TEXT,
    title            TEXT,
    explanation      TEXT,
    codebase_context TEXT,
    hotness_score    REAL,
    is_test          INTEGER NOT NULL DEFAULT 0   -- boolean
);
CREATE INDEX idx_pr_blocks_pr ON pr_blocks (pr_id);

-- Edge cases flagged per block.
CREATE TABLE pr_block_edge_cases (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    block_id    INTEGER NOT NULL REFERENCES pr_blocks (id) ON DELETE CASCADE,
    description TEXT,
    severity    TEXT                          -- low | medium | high
);
CREATE INDEX idx_pr_block_edge_cases_block ON pr_block_edge_cases (block_id);

-- Cross-PR relationships.
CREATE TABLE pr_links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id        INTEGER NOT NULL REFERENCES prs (id) ON DELETE CASCADE,
    linked_pr_id INTEGER NOT NULL REFERENCES prs (id) ON DELETE CASCADE,
    relationship TEXT,                         -- tests | tested_by | related | depends_on
    note         TEXT
);
CREATE INDEX idx_pr_links_pr ON pr_links (pr_id);
CREATE INDEX idx_pr_links_linked ON pr_links (linked_pr_id);

-- Chat messages per PR (optionally scoped to a block).
CREATE TABLE pr_chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pr_id      INTEGER NOT NULL REFERENCES prs (id) ON DELETE CASCADE,
    block_id   INTEGER REFERENCES pr_blocks (id) ON DELETE CASCADE,   -- null = PR-level
    role       TEXT    NOT NULL,
    content    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_pr_chat_messages_pr ON pr_chat_messages (pr_id);
