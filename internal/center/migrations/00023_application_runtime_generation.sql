-- +goose Up
ALTER TABLE agents ADD COLUMN runtime_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE applications ADD COLUMN runtime_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN runtime_generation INTEGER NOT NULL DEFAULT 0;

PRAGMA user_version = 23;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
