-- +goose Up
CREATE TABLE application_execution_retirements (
 application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE RESTRICT,
 replacement_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 evidence_json TEXT NOT NULL,
 verified_at TEXT NOT NULL,
 created_at TEXT NOT NULL
);
PRAGMA user_version=130;
