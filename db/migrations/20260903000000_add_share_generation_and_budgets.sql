-- +goose Up

-- Access generation (epoch): rotating it invalidates all media links derived
-- from earlier share pages. 0 = never rotated (legacy tokens, which carry no
-- epoch claim, remain valid).
ALTER TABLE share ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0;

-- Per-share concurrent playback budget (0 = unlimited). Applies to both raw
-- and transcoded streams served from the public share.
ALTER TABLE share ADD COLUMN max_concurrent_plays INTEGER NOT NULL DEFAULT 0;

-- Cumulative archive download budget (0 = unlimited). download_count is only
-- incremented after a zip archive has been fully delivered.
ALTER TABLE share ADD COLUMN max_downloads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE share ADD COLUMN download_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE share DROP COLUMN epoch;
ALTER TABLE share DROP COLUMN max_concurrent_plays;
ALTER TABLE share DROP COLUMN max_downloads;
ALTER TABLE share DROP COLUMN download_count;
