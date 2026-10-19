-- +goose Up
ALTER TABLE deployments ADD COLUMN source_receipt_sha256 TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 116;

-- +goose Down
SELECT 1;
