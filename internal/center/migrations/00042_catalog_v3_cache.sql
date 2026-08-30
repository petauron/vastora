-- +goose Up
-- Catalog responses are a renewable cache. Version 27 already persisted the
-- immutable manifest history, so discard only the superseded v2 transport and
-- let the configured source provide a freshly signed v3 envelope.
DELETE FROM catalog_cache
WHERE json_extract(CAST(envelope AS TEXT), '$.schemaVersion') = 2;

PRAGMA user_version = 42;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
