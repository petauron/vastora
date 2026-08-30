package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

const headscaleAPIKeyRenewalLead = 30 * 24 * time.Hour

type headscaleAPIKeyState struct {
	KeyID          uint64
	KeyPrefix      string
	ExpiresAt      time.Time
	State          string
	PreviousPrefix string
	LastError      string
}

func (s *Store) headscaleAPIKeyState(ctx context.Context) (headscaleAPIKeyState, bool, error) {
	var value headscaleAPIKeyState
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_prefix, expires_at, state, previous_prefix, last_error FROM headscale_api_keys WHERE id = 1`).Scan(
		&value.KeyID, &value.KeyPrefix, &expiresAt, &value.State, &value.PreviousPrefix, &value.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return headscaleAPIKeyState{}, false, nil
	}
	if err != nil {
		return headscaleAPIKeyState{}, false, err
	}
	if expiresAt != "" {
		value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			return headscaleAPIKeyState{}, false, errors.New("center: stored Headscale API key expiry is invalid")
		}
	}
	return value, true, nil
}

func (s *Store) bundledHeadscaleCredential(ctx context.Context) (endpoint, secretID, key string, configured bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT endpoint, secret_id FROM network_integrations
		WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&endpoint, &secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	value, err := s.getSecret(ctx, secretID, "integration:headscale")
	if err != nil {
		return "", "", "", false, err
	}
	return endpoint, secretID, string(value), true, nil
}

func (s *Store) beginHeadscaleAPIKeyRotation(ctx context.Context, previousPrefix string) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO headscale_api_keys(id, state, previous_prefix, updated_at)
		VALUES(1, 'preparing', ?, ?)
		ON CONFLICT(id) DO UPDATE SET state = 'preparing', previous_prefix = excluded.previous_prefix, last_error = '', updated_at = excluded.updated_at`, previousPrefix, now)
	return err
}

func (s *Store) acceptHeadscaleAPIKeyRotation(ctx context.Context, endpoint, expectedSecretID, previousPrefix string, rotation deployapi.HeadscaleAPIKeyRotation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentEndpoint, currentSecretID string
	if err := tx.QueryRowContext(ctx, `SELECT endpoint, secret_id FROM network_integrations
		WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&currentEndpoint, &currentSecretID); err != nil {
		return err
	}
	if currentEndpoint != endpoint || currentSecretID != expectedSecretID {
		return errors.New("center: bundled Headscale credential changed during rotation")
	}
	newSecretID, err := s.putSecret(ctx, tx, []byte(rotation.APIKey), "integration:headscale")
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	integrationUpdate, err := tx.ExecContext(ctx, `UPDATE network_integrations SET secret_id = ?, last_error = '', updated_at = ?
		WHERE kind = 'headscale' AND secret_id = ?`, newSecretID, now, expectedSecretID)
	if err != nil {
		return err
	}
	if changed, _ := integrationUpdate.RowsAffected(); changed != 1 {
		return errors.New("center: bundled Headscale credential changed during rotation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE headscale_api_keys SET key_id = ?, key_prefix = ?, expires_at = ?,
		state = 'committing', previous_prefix = ?, last_error = '', updated_at = ? WHERE id = 1 AND state = 'preparing'`,
		rotation.APIKeyID, rotation.APIKeyPrefix, rotation.APIKeyExpiresAt.UTC().Format(time.RFC3339Nano), previousPrefix, now)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Headscale API key rotation changed before commit")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, expectedSecretID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) finishHeadscaleAPIKeyRotation(ctx context.Context, currentPrefix, previousPrefix string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE headscale_api_keys SET state = 'ready', previous_prefix = '', last_error = '', updated_at = ?
		WHERE id = 1 AND state = 'committing' AND key_prefix = ? AND previous_prefix = ?`, s.now().UTC().Format(time.RFC3339Nano), currentPrefix, previousPrefix)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Headscale API key rotation changed before completion")
	}
	return nil
}

func (s *Store) recordHeadscaleAPIKeyRotationError(ctx context.Context, cause error) {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE headscale_api_keys SET last_error = ?, updated_at = ? WHERE id = 1`, message, s.now().UTC().Format(time.RFC3339Nano))
}

func (s *Server) MaintainHeadscaleAPIKey(ctx context.Context) error {
	rotator, ok := s.infrastructure.(deployapi.HeadscaleAPIKeyRotator)
	if !ok {
		return errors.New("center: deployment helper does not support Headscale API key rotation")
	}
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()
	endpoint, secretID, key, configured, err := s.store.bundledHeadscaleCredential(ctx)
	if err != nil || !configured {
		return err
	}
	currentPrefix, err := headscaleCredentialPrefix(key)
	if err != nil {
		return err
	}
	state, exists, err := s.store.headscaleAPIKeyState(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if err := s.store.beginHeadscaleAPIKeyRotation(ctx, currentPrefix); err != nil {
			return err
		}
		state = headscaleAPIKeyState{State: "preparing", PreviousPrefix: currentPrefix}
	} else if state.State == "ready" {
		if state.KeyPrefix != currentPrefix {
			return errors.New("center: stored Headscale API key metadata does not match the active credential")
		}
		if state.ExpiresAt.After(s.store.now().UTC().Add(headscaleAPIKeyRenewalLead)) {
			return nil
		}
		if err := s.store.beginHeadscaleAPIKeyRotation(ctx, currentPrefix); err != nil {
			return err
		}
		state.State = "preparing"
		state.PreviousPrefix = currentPrefix
	}
	if state.State == "preparing" {
		if state.PreviousPrefix == "" || state.PreviousPrefix != currentPrefix {
			return errors.New("center: pending Headscale API key rotation does not match the active credential")
		}
		rotation, err := rotator.PrepareHeadscaleAPIKeyRotation(ctx, deployapi.HeadscaleAPIKeyRotationRequest{CurrentPrefix: state.PreviousPrefix})
		if err != nil {
			s.store.recordHeadscaleAPIKeyRotationError(context.WithoutCancel(ctx), err)
			return err
		}
		httpClient, err := builtinHeadscaleHTTPClient(endpoint, s.store.builtinHeadscaleDialAddress, s.store.headscaleHTTPClient)
		if err == nil {
			err = (headscaleClient{baseURL: endpoint, apiKey: rotation.APIKey, http: httpClient}).verify(ctx)
		}
		if err != nil {
			s.store.recordHeadscaleAPIKeyRotationError(context.WithoutCancel(ctx), fmt.Errorf("center: verify replacement Headscale API key: %w", err))
			return err
		}
		if err := s.store.acceptHeadscaleAPIKeyRotation(ctx, endpoint, secretID, state.PreviousPrefix, rotation); err != nil {
			s.store.recordHeadscaleAPIKeyRotationError(context.WithoutCancel(ctx), err)
			return err
		}
		state = headscaleAPIKeyState{State: "committing", KeyPrefix: rotation.APIKeyPrefix, PreviousPrefix: state.PreviousPrefix}
		currentPrefix = rotation.APIKeyPrefix
	}
	if state.State != "committing" {
		return errors.New("center: stored Headscale API key rotation state is invalid")
	}
	if state.KeyPrefix == "" || state.PreviousPrefix == "" || state.KeyPrefix != currentPrefix {
		return errors.New("center: committing Headscale API key rotation does not match the active credential")
	}
	if err := rotator.CommitHeadscaleAPIKeyRotation(ctx, deployapi.HeadscaleAPIKeyCommitRequest{PreviousPrefix: state.PreviousPrefix, CurrentPrefix: state.KeyPrefix}); err != nil {
		s.store.recordHeadscaleAPIKeyRotationError(context.WithoutCancel(ctx), err)
		return err
	}
	return s.store.finishHeadscaleAPIKeyRotation(ctx, state.KeyPrefix, state.PreviousPrefix)
}

func (s *Server) RunHeadscaleAPIKeyMaintenance(ctx context.Context, interval time.Duration, report func(error)) {
	if interval < time.Hour {
		interval = time.Hour
	}
	run := func() {
		if err := s.MaintainHeadscaleAPIKey(ctx); err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func headscaleCredentialPrefix(key string) (string, error) {
	const marker = "hskey-api-"
	if strings.HasPrefix(key, marker) && len(key) > len(marker)+12 && key[len(marker)+12] == '-' {
		return key[len(marker) : len(marker)+12], nil
	}
	if prefix, _, found := strings.Cut(key, "."); found && len(prefix) == 7 {
		return prefix, nil
	}
	return "", errors.New("center: stored Headscale API key format is invalid")
}
