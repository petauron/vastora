-- +goose Up
-- Legacy REALITY inbounds have a shared traffic cap across every client on
-- that entry. Preserve that independent policy when Meridian takes authority.
ALTER TABLE meridian_endpoints ADD COLUMN total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes>=0);
ALTER TABLE meridian_endpoints ADD COLUMN used_bytes INTEGER NOT NULL DEFAULT 0 CHECK(used_bytes>=0);
ALTER TABLE meridian_endpoints ADD COLUMN quota_applied_enabled INTEGER NOT NULL DEFAULT 1 CHECK(quota_applied_enabled IN(0,1));
ALTER TABLE meridian_endpoints ADD COLUMN reset_day INTEGER NOT NULL DEFAULT 0 CHECK(reset_day BETWEEN 0 AND 31);
ALTER TABLE meridian_endpoints ADD COLUMN next_reset_at TEXT NOT NULL DEFAULT '';
ALTER TABLE meridian_endpoints ADD COLUMN last_reset_at TEXT NOT NULL DEFAULT '';
CREATE INDEX meridian_endpoints_reset ON meridian_endpoints(next_reset_at) WHERE reset_day>0 AND status<>'retired';
PRAGMA user_version = 91;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
