-- +goose Up
ALTER TABLE application_commands ADD COLUMN site_id TEXT NOT NULL DEFAULT '';
ALTER TABLE application_commands ADD COLUMN display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE;

UPDATE application_commands
SET site_id = COALESCE((SELECT application.site_id FROM applications application WHERE application.id = application_commands.application_id), ''),
    display_name = COALESCE(json_extract(CASE WHEN json_valid(input_json) THEN input_json ELSE '{}' END, '$.displayName'), '')
WHERE kind IN ('3xui.reality.create', '3xui.reality.rename');

CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx
ON application_commands(site_id, display_name COLLATE NOCASE)
WHERE site_id <> '' AND display_name <> ''
AND kind IN ('3xui.reality.create', '3xui.reality.rename')
AND (state IN ('pending', 'running') OR reconciliation_required = 1);

PRAGMA user_version = 19;
