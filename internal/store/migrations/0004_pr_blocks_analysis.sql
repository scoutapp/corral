-- Records which repo-analysis a PR's blocks were ranked against, so the UI can
-- detect when block hotness is stale (repo re-analyzed since, or blocks
-- extracted before any analysis). Compared to pr_repo_analysis.head_sha.
--   blocks_analysis_sha: the repo's analyzed head_sha at block-extraction time,
--     or NULL/'' when extracted with no analysis available (⇒ unranked).
--   blocks_extracted_at: when blocks were last (re)extracted.
ALTER TABLE prs ADD COLUMN blocks_analysis_sha TEXT;
ALTER TABLE prs ADD COLUMN blocks_extracted_at TEXT;
