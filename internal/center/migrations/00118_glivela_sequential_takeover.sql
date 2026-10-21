-- +goose Up
CREATE TABLE glivela_pilot_checkpoint_next (
    id INTEGER PRIMARY KEY,
    endpoint_id TEXT NOT NULL UNIQUE REFERENCES meridian_endpoints(id),
    checkpoint_json BLOB NOT NULL CHECK(json_valid(checkpoint_json)),
    updated_at TEXT NOT NULL
);
INSERT INTO glivela_pilot_checkpoint_next(id,endpoint_id,checkpoint_json,updated_at)
SELECT id,endpoint_id,checkpoint_json,updated_at FROM glivela_pilot_checkpoint;
DROP TABLE glivela_pilot_checkpoint;
ALTER TABLE glivela_pilot_checkpoint_next RENAME TO glivela_pilot_checkpoint;
PRAGMA user_version = 118;

-- +goose Down
SELECT 1;
