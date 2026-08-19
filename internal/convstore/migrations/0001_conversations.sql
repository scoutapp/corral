-- Captured Claude conversations (the conversations.db schema).
--
-- One `conversations` row per logical conversation, with ordered `conv_messages`
-- (text, tool_use, tool_result, result, …). Full-text search is served by an
-- FTS5 virtual table kept in sync by triggers — this DB is built with the
-- `sqlite_fts5` build tag (see install.sh + .goreleaser.*.yml). Unlike app_logs
-- (which avoids FTS5 to keep every build tag-free), the conversations DB opts in
-- because ranked deep-search across many conversations is the point of it.
--
-- Origin fields (project_id/project_label/repo_id/pr_number) are DENORMALIZED
-- with no FK to the app DB, so a conversation survives project deletion. trace_id
-- ties into the app_logs distributed trace; parent_conversation_id threads the
-- causal chain across an origin boundary (e.g. global chat → spawned project).
--
-- NOTE: transcripts can contain secrets (tokens, file contents, env). The DB
-- file is 0600 under the 0700 ~/.corral dir; we do not attempt lossy scrubbing.

CREATE TABLE conversations (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    conv_key               TEXT    NOT NULL UNIQUE,          -- stable upsert key (see conv_capture.go)
    claude_session_id      TEXT,                             -- Claude Code session id (once known)
    created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    -- Origin / linkage (denormalized; no FK to the app DB).
    origin_kind            TEXT    NOT NULL,                 -- global-chat|project-chat|pr-review-chat|merge|worker|issue-draft|prompt-draft|script-draft|analysis|log-analysis|sandbox
    origin_id              TEXT,                             -- job id / ws token / etc.
    project_id             TEXT,
    project_label          TEXT,                             -- copied so it survives project deletion
    repo_id                TEXT,
    pr_number              INTEGER,

    -- Trace / causal chain.
    trace_id               TEXT,                             -- app_logs distributed trace this began under
    root_span_id           TEXT,
    parent_conversation_id INTEGER,                          -- the conversation that spawned this one

    -- Summary / rollup.
    title                  TEXT,
    first_prompt           TEXT,
    model                  TEXT,
    total_cost_usd         REAL    NOT NULL DEFAULT 0,
    status                 TEXT    NOT NULL DEFAULT 'running',-- running|done|failed|canceled|interrupted
    message_count          INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_conversations_id       ON conversations (id DESC);
CREATE INDEX idx_conversations_origin   ON conversations (origin_kind, id DESC);
CREATE INDEX idx_conversations_project  ON conversations (project_id, id DESC);
CREATE INDEX idx_conversations_trace    ON conversations (trace_id);
CREATE INDEX idx_conversations_parent   ON conversations (parent_conversation_id);
CREATE INDEX idx_conversations_created  ON conversations (created_at);  -- age pruning

CREATE TABLE conv_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,                         -- order within the conversation
    ts              TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    role            TEXT    NOT NULL,                         -- user|assistant|system
    type            TEXT    NOT NULL,                         -- == chatServerMsg.Type (text|tool_use|tool_result|result|error|…)
    text            TEXT,                                     -- assistant/user text or error message
    tool_name       TEXT,
    tool_input      TEXT,                                     -- tool_use input (JSON)
    tool_result     TEXT,                                     -- tool_result payload
    cost_usd        TEXT,
    model           TEXT,
    is_error        INTEGER NOT NULL DEFAULT 0,
    meta_json       TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_conv_messages_conv ON conv_messages (conversation_id, seq);
CREATE INDEX idx_conv_messages_id   ON conv_messages (id DESC);

-- FTS5 index over the searchable text of each message. `content=` makes it an
-- external-content table (mirrors conv_messages, no duplicate row storage); the
-- triggers below keep it in sync. Search MATCHes here, then joins back to
-- conv_messages / conversations by rowid (= conv_messages.id).
CREATE VIRTUAL TABLE conv_messages_fts USING fts5(
    text, tool_input, tool_result,
    content='conv_messages',
    content_rowid='id',
    tokenize='porter unicode61'
);

-- Keep the FTS index in lockstep with conv_messages.
CREATE TRIGGER conv_messages_ai AFTER INSERT ON conv_messages BEGIN
    INSERT INTO conv_messages_fts (rowid, text, tool_input, tool_result)
    VALUES (new.id, new.text, new.tool_input, new.tool_result);
END;
CREATE TRIGGER conv_messages_ad AFTER DELETE ON conv_messages BEGIN
    INSERT INTO conv_messages_fts (conv_messages_fts, rowid, text, tool_input, tool_result)
    VALUES ('delete', old.id, old.text, old.tool_input, old.tool_result);
END;
CREATE TRIGGER conv_messages_au AFTER UPDATE ON conv_messages BEGIN
    INSERT INTO conv_messages_fts (conv_messages_fts, rowid, text, tool_input, tool_result)
    VALUES ('delete', old.id, old.text, old.tool_input, old.tool_result);
    INSERT INTO conv_messages_fts (rowid, text, tool_input, tool_result)
    VALUES (new.id, new.text, new.tool_input, new.tool_result);
END;
