-- +goose Up
-- Only exact official defaults are product-owned. Never replace a substring in
-- a custom name or rewrite signed manifests, application keys or runtime state.
UPDATE applications SET name = 'Glivela'
WHERE app_key = 'vastora-official/meridian' AND name = 'Meridian';

UPDATE applications SET name = CASE name
    WHEN 'Pulse' THEN 'Senulun'
    WHEN 'Pulse 监控主机' THEN 'Senulun 监控主机'
END
WHERE app_key = 'vastora-official/pulse' AND name IN ('Pulse', 'Pulse 监控主机');

UPDATE applications SET name = CASE name
    WHEN 'Pulse Agent' THEN 'Senulun Agent'
    WHEN 'Pulse 探针' THEN 'Senulun 探针'
END
WHERE app_key = 'vastora-official/pulse-agent' AND name IN ('Pulse Agent', 'Pulse 探针');

PRAGMA user_version = 110;
