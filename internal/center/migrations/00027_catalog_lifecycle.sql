-- +goose Up
ALTER TABLE catalog_sources ADD COLUMN last_checked_at TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_sources ADD COLUMN generation TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_sources ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

UPDATE catalog_sources SET generation = lower(hex(randomblob(16)));

UPDATE catalog_sources
SET last_checked_at = COALESCE(
    (SELECT fetched_at FROM catalog_cache WHERE catalog_cache.source_id = catalog_sources.id),
    ''
);

CREATE TABLE catalog_manifest_history (
    source_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    version TEXT NOT NULL,
    manifest_sha256 TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    PRIMARY KEY(source_id, app_id, version)
);

PRAGMA user_version = 27;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
