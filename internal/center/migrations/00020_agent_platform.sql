-- +goose Up
ALTER TABLE agents ADD COLUMN operating_system TEXT NOT NULL DEFAULT 'linux' CHECK(operating_system = 'linux');
ALTER TABLE agents ADD COLUMN architecture TEXT NOT NULL DEFAULT 'amd64' CHECK(architecture IN ('amd64', 'arm64'));

PRAGMA user_version = 20;
