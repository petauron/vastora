-- +goose Up
CREATE TABLE official_app_ui_assets (
 app_id TEXT NOT NULL CHECK(app_id = 'meridian'),
 asset_kind TEXT NOT NULL CHECK(asset_kind IN ('script', 'style')),
 app_version TEXT NOT NULL,
 target_name TEXT NOT NULL UNIQUE,
 catalog_revision INTEGER NOT NULL CHECK(catalog_revision > 0),
 sha256 TEXT NOT NULL,
 bundle BLOB NOT NULL CHECK(length(bundle) BETWEEN 1 AND 4194304),
 PRIMARY KEY(app_id, asset_kind)
);
CREATE TABLE official_app_ui_history (
 app_id TEXT NOT NULL CHECK(app_id = 'meridian'),
 app_version TEXT NOT NULL,
 asset_kind TEXT NOT NULL CHECK(asset_kind IN ('script', 'style')),
 sha256 TEXT NOT NULL,
 PRIMARY KEY(app_id, app_version, asset_kind)
);
PRAGMA user_version = 101;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
