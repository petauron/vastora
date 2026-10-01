-- +goose Up
-- Historical link tasks did not carry per-probe authentication or transport
-- evidence. Retain the records, but never resume or rank those samples as a
-- verified link test. Other diagnostic kinds remain untouched.
UPDATE node_diagnostic_checks
SET state='failed', error='transport_unverified', lease_expires_at=''
WHERE kind IN ('meridian.link-bandwidth','meridian.link-bandwidth-server')
  AND json_extract(targets_json,'$.sealedAuth') IS NULL;

PRAGMA user_version = 103;
