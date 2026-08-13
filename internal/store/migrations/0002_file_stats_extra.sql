-- Extra per-file forensics for the PR review page's file-stats panel:
--   author_count  — distinct commit-author emails (is the author the sole contributor?)
--   first_commit  — unix ts of the file's first commit (staleness / age)
--   last_commit   — unix ts of the file's most recent commit (staleness)
-- Backfilled by the next `Analyze` run; existing rows default to 0/NULL until then.
ALTER TABLE pr_file_stats ADD COLUMN author_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pr_file_stats ADD COLUMN first_commit INTEGER;
ALTER TABLE pr_file_stats ADD COLUMN last_commit INTEGER;
