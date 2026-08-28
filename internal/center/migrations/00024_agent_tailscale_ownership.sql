-- +goose Up
ALTER TABLE agents ADD COLUMN tailscale_ownership TEXT NOT NULL DEFAULT '' CHECK(tailscale_ownership IN ('', 'managed', 'external'));

PRAGMA user_version = 24;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
