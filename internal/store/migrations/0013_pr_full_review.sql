-- Full PR review: the markdown result of the "Full Review" workflow (risk +
-- heuristics + block findings fed into the editable pr.review prompt). Stored
-- separately from ai_analysis (which holds the risk JSON verdict) so the two
-- don't clobber each other. Latest-wins: a re-run overwrites this. reviewed_at
-- lets the UI show staleness.
ALTER TABLE prs ADD COLUMN full_review TEXT;
ALTER TABLE prs ADD COLUMN full_review_at TEXT;
