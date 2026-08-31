-- +goose Up
ALTER TABLE publications ADD COLUMN path_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE publications ADD COLUMN access_application_id TEXT NOT NULL DEFAULT '';
ALTER TABLE routes ADD COLUMN path_prefix TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX publications_hostname_path_idx ON publications(hostname, path_prefix) WHERE path_prefix <> '';

PRAGMA user_version = 45;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
