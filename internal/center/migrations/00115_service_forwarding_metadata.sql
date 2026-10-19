-- +goose Up
ALTER TABLE services ADD COLUMN proxy_protocol TEXT NOT NULL DEFAULT '' CHECK(proxy_protocol IN ('','v2'));

-- Preserve the already authorized wire format once. Runtime decisions after
-- this migration read generic service metadata, never product/component names.
UPDATE services SET proxy_protocol='v2'
WHERE app_protocol='meridian/entry'
   OR (app_protocol='vless/tcp/reality' AND EXISTS (
       SELECT 1 FROM three_x_ui_reality_guards guard
       WHERE guard.service_id=services.id AND guard.status='ready'
   ));

PRAGMA user_version = 115;

-- +goose Down
SELECT 1;
