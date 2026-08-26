-- +goose Up
CREATE TABLE system_endpoint_aliases (
    kind TEXT NOT NULL CHECK(kind IN ('center', 'headscale')),
    endpoint TEXT NOT NULL,
    certificate_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
    certificate_not_after TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(kind, endpoint)
);

PRAGMA user_version = 21;
