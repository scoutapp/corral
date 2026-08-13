-- The PR description/body (markdown), shown on the PR review page. Backfilled
-- on the next fetch/view of the PR.
ALTER TABLE prs ADD COLUMN body TEXT;
