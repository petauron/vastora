-- +goose Up
CREATE TABLE IF NOT EXISTS storage_key_binding (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    sealed BLOB NOT NULL
);
PRAGMA user_version = 36;
