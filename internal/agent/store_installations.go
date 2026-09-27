package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/secret"
)

func (s *Store) RecordApplied(ctx context.Context, installation AppliedInstallation) (InstallationStatus, error) {
	if strings.TrimSpace(installation.InstanceID) == "" || strings.TrimSpace(installation.AppKey) == "" || strings.TrimSpace(installation.Version) == "" {
		return InstallationStatus{}, errors.New("agent: instance id, app key, and version are required")
	}
	if !strings.Contains(installation.AppKey, "/") {
		return InstallationStatus{}, errors.New("agent: app key must be source-id/app-id")
	}
	canonicalConfig, err := canonicalJSONObject(installation.Config)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: configuration: %w", err)
	}
	canonicalSecrets, err := canonicalJSONObject(installation.Secrets)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: secrets: %w", err)
	}
	if installation.Manifest.ID != "" {
		if err := catalog.ValidateApp(installation.Manifest); err != nil || !strings.HasSuffix(installation.AppKey, "/"+installation.Manifest.ID) || installation.Version != installation.Manifest.Version {
			return InstallationStatus{}, errors.New("agent: applied application manifest is invalid")
		}
	}
	configHash := sha256.Sum256(canonicalConfig)
	encodedState, err := json.Marshal(sealedApplicationState{
		ApplicationID: installation.ApplicationID, Config: canonicalConfig, Secrets: canonicalSecrets, ServiceAddress: installation.ServiceAddress,
		Manifest: installation.Manifest, ApplicationRole: installation.ApplicationRole,
	})
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: encode applied application state: %w", err)
	}
	sealedState, err := secret.Seal(s.key, encodedState, applicationStateContext(installation.InstanceID))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: encrypt applied application state: %w", err)
	}
	now := s.now().UTC()
	status := InstallationStatus{InstanceID: installation.InstanceID, AppKey: installation.AppKey, Version: installation.Version, ConfigHash: hex.EncodeToString(configHash[:]), AppliedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: begin applied state update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key = ? AND instance_id <> ?`, installation.AppKey, installation.InstanceID); err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: replace previous applied state: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO applied_installations(instance_id, app_key, version, sealed_state, config_hash, applied_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET app_key=excluded.app_key, version=excluded.version,
		sealed_state=excluded.sealed_state, config_hash=excluded.config_hash, applied_at=excluded.applied_at`, status.InstanceID, status.AppKey, status.Version, sealedState, status.ConfigHash, status.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: record applied state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: commit applied state: %w", err)
	}
	return status, nil
}

func (s *Store) ListApplied(ctx context.Context) ([]InstallationStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, app_key, version, config_hash, applied_at FROM applied_installations ORDER BY instance_id`)
	if err != nil {
		return nil, fmt.Errorf("agent: list applied state: %w", err)
	}
	defer rows.Close()
	var statuses []InstallationStatus
	for rows.Next() {
		var status InstallationStatus
		var appliedAt string
		if err := rows.Scan(&status.InstanceID, &status.AppKey, &status.Version, &status.ConfigHash, &appliedAt); err != nil {
			return nil, fmt.Errorf("agent: scan applied state: %w", err)
		}
		status.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
		if err != nil {
			return nil, fmt.Errorf("agent: parse applied time: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s *Store) AppliedConfig(ctx context.Context, appKey string) (json.RawMessage, error) {
	installation, err := s.AppliedInstallation(ctx, appKey)
	if err != nil {
		return nil, err
	}
	return installation.Config, nil
}

func (s *Store) AppliedInstallation(ctx context.Context, appKey string) (AppliedInstallation, error) {
	var value AppliedInstallation
	var sealedState []byte
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT instance_id, app_key, version, sealed_state, config_hash, applied_at FROM applied_installations WHERE app_key = ? ORDER BY applied_at DESC LIMIT 1`, appKey).Scan(&value.InstanceID, &value.AppKey, &value.Version, &sealedState, &value.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AppliedInstallation{}, errApplicationNotInstalled
	}
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: read applied application: %w", err)
	}
	state, err := s.openApplicationState(value.InstanceID, sealedState)
	if err != nil {
		return AppliedInstallation{}, err
	}
	value.Config = state.Config
	value.ApplicationID = state.ApplicationID
	value.Secrets = state.Secrets
	value.ServiceAddress = state.ServiceAddress
	value.Manifest = state.Manifest
	value.ApplicationRole = state.ApplicationRole
	if value.Manifest.ID != "" {
		// Historical v3 manifests remain encrypted audit evidence. They are not
		// executable recipes; the v4 executor requires one-shot verified adoption.
		legacyEvidence := value.Manifest.PackageRevision == 0 && value.Manifest.Runtime == nil
		if (!legacyEvidence && catalog.ValidateApp(value.Manifest) != nil) || !strings.HasSuffix(value.AppKey, "/"+value.Manifest.ID) || value.Version != value.Manifest.Version {
			return AppliedInstallation{}, errors.New("agent: persisted application manifest is invalid")
		}
	}
	value.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: parse applied application time: %w", err)
	}
	return value, nil
}

func (s *Store) openApplicationState(instanceID string, sealedState []byte) (sealedApplicationState, error) {
	plain, err := secret.Open(s.key, sealedState, applicationStateContext(instanceID))
	if err != nil {
		return sealedApplicationState{}, fmt.Errorf("agent: decrypt applied application state: %w", err)
	}
	var state sealedApplicationState
	if json.Unmarshal(plain, &state) != nil {
		return sealedApplicationState{}, errors.New("agent: applied application state is invalid")
	}
	if _, err := canonicalJSONObject(state.Config); err != nil {
		return sealedApplicationState{}, errors.New("agent: applied application configuration is invalid")
	}
	if _, err := canonicalJSONObject(state.Secrets); err != nil {
		return sealedApplicationState{}, errors.New("agent: applied application secrets are invalid")
	}
	return state, nil
}

// RestorableInstallations returns the encrypted last-known-good application
// states in deterministic order. Secret bytes are decrypted only into the
// returned task material and never written to logs or Docker configuration.
func (s *Store) RestorableInstallations(ctx context.Context) ([]AppliedInstallation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT app_key FROM applied_installations ORDER BY app_key`)
	if err != nil {
		return nil, fmt.Errorf("agent: list restorable applications: %w", err)
	}
	defer rows.Close()
	var appKeys []string
	for rows.Next() {
		var appKey string
		if err := rows.Scan(&appKey); err != nil {
			return nil, fmt.Errorf("agent: scan restorable application: %w", err)
		}
		appKeys = append(appKeys, appKey)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent: list restorable applications: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("agent: close restorable application list: %w", err)
	}
	installations := make([]AppliedInstallation, 0, len(appKeys))
	for _, appKey := range appKeys {
		installation, err := s.AppliedInstallation(ctx, appKey)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	return installations, nil
}

func (s *Store) RemoveApplied(ctx context.Context, appKey string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Called only after successful application removal under landingMutationMu.
	// Old tokens/ledger ownership must not attach to a new controller install.
	if proxyRuntimeApp(appKey) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM landing_controller_state`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key = ?`, appKey); err != nil {
		return fmt.Errorf("agent: remove applied state: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ReadAppliedSecrets(ctx context.Context, instanceID string) (json.RawMessage, error) {
	var sealedState []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM applied_installations WHERE instance_id = ?`, instanceID).Scan(&sealedState); err != nil {
		return nil, fmt.Errorf("agent: read applied secrets: %w", err)
	}
	state, err := s.openApplicationState(instanceID, sealedState)
	if err != nil {
		return nil, err
	}
	return state.Secrets, nil
}

func canonicalJSONObject(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("must be a JSON object")
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	return json.Marshal(value)
}
