-- Distributed tracing over app_logs. Every root action (a chat turn, a flow run,
-- a scheduler tick, an HTTP request) mints a trace_id; work it causes downstream
-- carries that trace_id plus its own span_id and the parent_span_id it descends
-- from. That lets the Logs tab reconstruct the causal tree: chat → called Claude
-- → Claude called a flow → the flow started a sandbox.
--
-- Timing model: OPEN/CLOSE span pairs. A span emits TWO app_logs rows sharing one
-- span_id — a start row (event ending in `.start`) at T0 and an end row (`.end`)
-- at T1 carrying the final status. Wall-clock is end.ts - start.ts, so a span
-- that outlives a single synchronous call (an async flow step, a long AI call)
-- still gets precise timing. Rows that are neither a start nor an end are plain
-- point-in-time logs that may still carry trace_id/span_id to place them in the
-- tree.
--
-- Retention note: pruning (by age or row cap, migration 0008) can delete one half
-- of a pair at the retention boundary — an old start whose end survives, or an end
-- whose start aged out. The trace viewer tolerates this: a start with no end is an
-- UNTERMINATED span (still running, or its end was pruned); an end with no start
-- is shown at its own ts with unknown duration. We deliberately do NOT try to keep
-- pairs together during prune — it would complicate the cheap id/age deletes for a
-- cosmetic edge only visible at the very oldest rows.

ALTER TABLE app_logs ADD COLUMN trace_id       TEXT;  -- root-action id; null for untraced rows
ALTER TABLE app_logs ADD COLUMN span_id        TEXT;  -- this span's id (shared by its start+end rows)
ALTER TABLE app_logs ADD COLUMN parent_span_id TEXT;  -- the span this one descends from; null at the root

-- Pull a whole trace back in one indexed scan (viewer: "show me this tree").
CREATE INDEX idx_app_logs_trace ON app_logs (trace_id, id);

-- Reconcile a span's start row with its end row by span_id.
CREATE INDEX idx_app_logs_span ON app_logs (span_id);
