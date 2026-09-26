-- +goose Up
ALTER TABLE agents ADD COLUMN landing_egress_addresses_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(landing_egress_addresses_json));
PRAGMA user_version = 98;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
