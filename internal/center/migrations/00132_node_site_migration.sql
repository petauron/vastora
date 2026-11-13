-- +goose Up
ALTER TABLE node_sites ADD COLUMN migration_from TEXT NOT NULL DEFAULT '';
ALTER TABLE node_sites ADD COLUMN migration_previous BLOB NOT NULL DEFAULT X'';
PRAGMA user_version=132;
