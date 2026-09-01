-- +goose Up
-- VPS traffic allowances normally renew on a calendar billing day. Replace
-- the old fixed-day interval with one monthly day (1-31; 31 means month-end
-- when the month is shorter). Existing recurring plans keep their already
-- scheduled next reset, then continue on the first day of following months.
DROP INDEX three_x_ui_inbound_plans_due_idx;
CREATE TABLE three_x_ui_inbound_plans_v47 (
    service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    inbound_tag TEXT NOT NULL,
    total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes >= 0),
    reset_day INTEGER NOT NULL DEFAULT 0 CHECK(reset_day BETWEEN 0 AND 31),
    next_reset_at TEXT NOT NULL DEFAULT '',
    last_reset_at TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'resetting', 'failed')),
    retry_at TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
INSERT INTO three_x_ui_inbound_plans_v47(
    service_id, inbound_tag, total_bytes, reset_day, next_reset_at,
    last_reset_at, revision, status, retry_at, attempt, last_error, updated_at
)
SELECT service_id, inbound_tag, total_bytes,
       CASE WHEN reset_days > 0 THEN 1 ELSE 0 END,
       next_reset_at, last_reset_at, revision, status, retry_at, attempt,
       last_error, updated_at
FROM three_x_ui_inbound_plans;
DROP TABLE three_x_ui_inbound_plans;
ALTER TABLE three_x_ui_inbound_plans_v47 RENAME TO three_x_ui_inbound_plans;
CREATE INDEX three_x_ui_inbound_plans_due_idx
ON three_x_ui_inbound_plans(status, next_reset_at, retry_at)
WHERE reset_day > 0;

-- Commands serialized with the retired fixed-day contract must be retried so
-- neither Center nor Agent can interpret an interval as a day of the month.
UPDATE application_commands
SET state = 'failed',
    lease_expires_at = '',
    reconciliation_required = 0,
    error = 'Center was upgraded; retry this 3x-ui operation with the monthly traffic plan settings',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind IN ('3xui.reality.create', '3xui.clients.manage')
  AND (state IN ('pending', 'running') OR reconciliation_required = 1);

PRAGMA user_version = 47;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
