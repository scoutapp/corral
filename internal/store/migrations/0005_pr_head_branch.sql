-- The PR's head branch name (headRefName), so a project can be started on the
-- PR's branch to verify it. Backfilled on the next fetch/view of the PR.
ALTER TABLE prs ADD COLUMN head_ref TEXT;
