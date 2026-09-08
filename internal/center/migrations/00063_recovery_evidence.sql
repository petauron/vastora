-- +goose Up
CREATE TABLE recovery_evidence (
    component_key TEXT PRIMARY KEY,
    artifact_json BLOB NOT NULL CHECK(json_valid(artifact_json)),
    verified_at TEXT NOT NULL
);
PRAGMA user_version = 63;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
