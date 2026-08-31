-- +goose Up
ALTER TABLE agents ADD COLUMN public_egress_address TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN public_egress_bind_address TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN public_egress_mode TEXT NOT NULL DEFAULT '' CHECK(public_egress_mode IN ('', 'direct', 'nat'));
ALTER TABLE agents ADD COLUMN public_egress_observed_at TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 44;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
