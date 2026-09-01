-- +goose Up
INSERT OR IGNORE INTO settings(key, value)
SELECT 'headscale_dns_policy', 'custom'
WHERE EXISTS (
  SELECT 1 FROM network_integrations
  WHERE kind = 'headscale' AND mode = 'builtin'
);

INSERT OR IGNORE INTO settings(key, value)
SELECT 'headscale_dns_resolvers', '["1.1.1.1","1.0.0.1"]'
WHERE EXISTS (
  SELECT 1 FROM network_integrations
  WHERE kind = 'headscale' AND mode = 'builtin'
);

PRAGMA user_version = 52;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
