-- Give every conversation a stable, user-facing UUID.
--
-- The integer id is an internal autoincrement (fine for joins), but it's not a
-- durable public handle: it isn't unique across machines and reads as "row 42".
-- claude_session_id is a real UUID but arrives late (blank until the first
-- session frame) and can change on reset; conv_key is an internal upsert key
-- (for global chats it's a Go pointer string). So we add a dedicated uuid,
-- assigned once at conversation creation and never changed. It's what the UI
-- shows at the top of a host chat and what a user can copy to reference a
-- conversation.
--
-- Additive + backfilled: existing rows get a deterministic (id-derived)
-- placeholder UUID so the NOT-NULL/UNIQUE guarantees hold immediately; new rows
-- get a random v4 from the app (see convstore.newUUID).

ALTER TABLE conversations ADD COLUMN uuid TEXT;

-- Backfill existing rows with a stable, unique, UUID-shaped value derived from
-- the row id. Not a real random v4 (these predate the feature), but well-formed
-- and unique, so the index below is satisfiable. The '5' version nibble marks
-- these as backfilled (v4 rows the app writes use '4').
UPDATE conversations
SET uuid = '00000000-0000-5000-8000-' || substr('000000000000' || printf('%x', id), -12, 12)
WHERE uuid IS NULL OR uuid = '';

CREATE UNIQUE INDEX idx_conversations_uuid ON conversations (uuid);
