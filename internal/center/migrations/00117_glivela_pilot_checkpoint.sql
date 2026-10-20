-- +goose Up
CREATE TABLE glivela_pilot_checkpoint (
    id INTEGER PRIMARY KEY CHECK(id=1),
    endpoint_id TEXT NOT NULL UNIQUE REFERENCES meridian_endpoints(id),
    checkpoint_json BLOB NOT NULL CHECK(json_valid(checkpoint_json)),
    updated_at TEXT NOT NULL
);
PRAGMA user_version = 117;

-- +goose Down
SELECT 1;
