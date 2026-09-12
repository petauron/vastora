-- +goose Up
-- Trust state deliberately has no FK to catalog_sources: deleting a source
-- must not erase its accepted revision or authorized root rotations.
CREATE TABLE official_catalog_trust (
    channel TEXT PRIMARY KEY,
    -- Revision zero retains verified root updates before any target is accepted.
    revision INTEGER NOT NULL CHECK(revision >= 0),
    target_sha256 TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    metadata_json BLOB NOT NULL,
    target BLOB NOT NULL
);
-- Unissued upgrades preserve their observed application state. Existing rows
-- have no snapshot, so they must never restore a presumed running instance.
ALTER TABLE deployments ADD COLUMN pre_dispatch_application_status TEXT NOT NULL DEFAULT 'failed'
    CHECK(pre_dispatch_application_status IN ('running', 'failed', 'stopped'));
-- Old locally signed cache is not upstream trust. Keep application manifests
-- and immutable content history; remove only the superseded source cache.
DELETE FROM catalog_cache WHERE source_id = 'vastora-official';
DELETE FROM settings WHERE key = 'official_catalog_signing_key';

PRAGMA user_version = 72;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
