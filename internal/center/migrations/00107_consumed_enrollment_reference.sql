-- +goose Up
-- Consumed one-time commands no longer need their deleted bootstrap secret.
-- Keep the token and replay audit; never repair an unused command implicitly.
UPDATE agent_enrollment_tokens SET bootstrap_secret_id = NULL
WHERE used_at IS NOT NULL AND bootstrap_secret_id IS NOT NULL
AND NOT EXISTS (SELECT 1 FROM secrets WHERE secrets.id = agent_enrollment_tokens.bootstrap_secret_id);
PRAGMA user_version = 107;
