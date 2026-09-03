-- +goose Up
-- Scan plans are reviewable change previews (dry-runs) of media library scans.
-- A plan binds the scan scope, intensity, relevant config and filesystem/database
-- fingerprints captured at preview time; it is applied only after explicit admin
-- confirmation, and rejected when the bound facts have changed.
CREATE TABLE IF NOT EXISTS scan_plan (
    id                 TEXT PRIMARY KEY,
    status             TEXT NOT NULL DEFAULT 'running', -- running|ready|applying|applied|failed
    full_scan          BOOLEAN NOT NULL DEFAULT false,
    targets            TEXT NOT NULL DEFAULT '[]',      -- JSON array of {libraryId, folderPath}
    library_ids        TEXT NOT NULL DEFAULT '[]',      -- JSON array of library IDs in scope
    config_fingerprint TEXT NOT NULL DEFAULT '',
    config_snapshot    TEXT NOT NULL DEFAULT '{}',      -- human-readable relevant config
    fs_fingerprint     TEXT NOT NULL DEFAULT '',
    db_fingerprint     TEXT NOT NULL DEFAULT '{}',      -- library/playlist facts at preview time
    summary            TEXT NOT NULL DEFAULT '{}',      -- JSON PlanSummary
    warnings_total     INTEGER NOT NULL DEFAULT 0,
    error              TEXT NOT NULL DEFAULT '',
    expires_at         DATETIME,
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL,
    applied_at         DATETIME
);

CREATE INDEX IF NOT EXISTS scan_plan_status_idx ON scan_plan(status);
CREATE INDEX IF NOT EXISTS scan_plan_created_at_idx ON scan_plan(created_at);

-- Individual change entries collected during a preview. Items are deleted
-- explicitly by the repository when the parent plan is deleted.
CREATE TABLE IF NOT EXISTS scan_plan_item (
    id            TEXT PRIMARY KEY,
    plan_id       TEXT NOT NULL,
    library_id    INTEGER,
    change_type   TEXT NOT NULL, -- added|updated|moved|missing|purge|playlist|warning
    item_type     TEXT NOT NULL DEFAULT 'media', -- media|playlist|warning
    media_file_id TEXT NOT NULL DEFAULT '',
    path          TEXT NOT NULL DEFAULT '',
    old_path      TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    artist        TEXT NOT NULL DEFAULT '',
    album         TEXT NOT NULL DEFAULT '',
    details       TEXT NOT NULL DEFAULT '{}', -- JSON, type-specific extra info
    created_at    DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS scan_plan_item_plan_idx ON scan_plan_item(plan_id);
CREATE INDEX IF NOT EXISTS scan_plan_item_plan_type_idx ON scan_plan_item(plan_id, change_type);
CREATE INDEX IF NOT EXISTS scan_plan_item_plan_lib_idx ON scan_plan_item(plan_id, library_id);

-- +goose Down
DROP TABLE IF EXISTS scan_plan_item;
DROP TABLE IF EXISTS scan_plan;
