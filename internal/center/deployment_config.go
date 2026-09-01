package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/secret"
)

func removeJSONObjectKeys(raw json.RawMessage, keys ...string) (json.RawMessage, error) {
	values, err := decodeJSONObject(raw, "center: deployment configuration must be a JSON object")
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		delete(values, key)
	}
	return json.Marshal(values)
}

func decodeJSONObject(raw json.RawMessage, message string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, errors.New(message)
	}
	return values, nil
}

func (s *Store) mergePreviousDeploymentConfig(ctx context.Context, agentID, appKey string, updates json.RawMessage) (json.RawMessage, error) {
	var deploymentID string
	var configJSON []byte
	var secretID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT d.id, d.config_json, d.secret_id FROM deployments d
			WHERE d.agent_id = ? AND d.app_key = ? AND d.state = 'succeeded' AND d.operation IN ('install', 'upgrade', 'configure')
		AND NOT EXISTS (
			SELECT 1 FROM deployments removed
			WHERE removed.agent_id = d.agent_id AND removed.app_key = d.app_key
			AND removed.state = 'succeeded' AND removed.operation = 'uninstall' AND removed.created_at > d.created_at
		)
		ORDER BY d.updated_at DESC, d.rowid DESC LIMIT 1`, agentID, appKey).Scan(&deploymentID, &configJSON, &secretID); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: upgrade requires an existing successful installation")
	} else if err != nil {
		return nil, fmt.Errorf("center: read previous deployment configuration: %w", err)
	}
	merged, err := decodeJSONObject(configJSON, "center: stored deployment configuration is invalid")
	if err != nil {
		return nil, errors.New("center: stored deployment configuration is invalid")
	}
	if secretID.Valid {
		secretJSON, err := s.getSecret(ctx, secretID.String, "deployment:"+deploymentID)
		if err != nil {
			return nil, err
		}
		secretValues, decodeErr := decodeJSONObject(secretJSON, "center: stored deployment secrets are invalid")
		if decodeErr != nil {
			return nil, errors.New("center: stored deployment secrets are invalid")
		}
		for key, value := range secretValues {
			merged[key] = value
		}
	}
	if len(updates) != 0 {
		changed, decodeErr := decodeJSONObject(updates, "center: deployment configuration must be a JSON object")
		if decodeErr != nil {
			return nil, decodeErr
		}
		for key, value := range changed {
			merged[key] = value
		}
	}
	result, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("center: merge deployment configuration: %w", err)
	}
	return result, nil
}

type cpaCredentialValues struct {
	ManagementKey string `json:"management_key"`
	APIKey        string `json:"api_key"`
}

func decodeCPACredentialValues(raw []byte) (cpaCredentialValues, error) {
	var values cpaCredentialValues
	if json.Unmarshal(raw, &values) != nil || strings.TrimSpace(values.ManagementKey) == "" || strings.TrimSpace(values.APIKey) == "" || values.ManagementKey == values.APIKey {
		return cpaCredentialValues{}, errors.New("center: stored CPA credentials are invalid")
	}
	return values, nil
}

func (s *Store) currentCPACredentials(ctx context.Context, agentID string) (cpaCredentialValues, error) {
	var deploymentID, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT id, secret_id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade', 'configure') AND secret_id IS NOT NULL ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, cpaAppKey).Scan(&deploymentID, &secretID); errors.Is(err, sql.ErrNoRows) {
		return cpaCredentialValues{}, errors.New("center: previous CPA credentials were not found")
	} else if err != nil {
		return cpaCredentialValues{}, fmt.Errorf("center: read CPA deployment: %w", err)
	}
	plain, err := s.getSecret(ctx, secretID, "deployment:"+deploymentID)
	if err != nil {
		return cpaCredentialValues{}, err
	}
	return decodeCPACredentialValues(plain)
}

func (s *Store) withCPACredentials(ctx context.Context, agentID, operation string, overrides *cpaCredentialValues) ([]byte, error) {
	var values cpaCredentialValues
	var err error
	if operation == "install" {
		managementKey, tokenErr := randomToken(32)
		if tokenErr != nil {
			return nil, tokenErr
		}
		apiKey, tokenErr := randomToken(32)
		if tokenErr != nil {
			return nil, tokenErr
		}
		values = cpaCredentialValues{ManagementKey: managementKey, APIKey: apiKey}
	} else {
		values, err = s.currentCPACredentials(ctx, agentID)
		if err != nil {
			return nil, err
		}
	}
	if overrides != nil {
		values = *overrides
	}
	if strings.TrimSpace(values.ManagementKey) == "" || strings.TrimSpace(values.APIKey) == "" || values.ManagementKey == values.APIKey {
		return nil, errors.New("center: CPA management and client credentials must be distinct non-empty values")
	}
	return json.Marshal(values)
}

func (s *Store) withCPASiteTimezone(ctx context.Context, agentID string, raw json.RawMessage) (json.RawMessage, error) {
	values, err := decodeJSONObject(raw, "center: CPA configuration must be a JSON object")
	if err != nil {
		return nil, err
	}
	var timezone string
	if err := s.db.QueryRowContext(ctx, `SELECT site.timezone FROM agents agent JOIN sites site ON site.id = agent.site_id WHERE agent.id = ?`, agentID).Scan(&timezone); err != nil {
		return nil, fmt.Errorf("center: read CPA Site timezone: %w", err)
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return nil, errors.New("center: CPA Site timezone is unavailable")
	}
	encodedTimezone, err := json.Marshal(timezone)
	if err != nil {
		return nil, err
	}
	values["timezone"] = encodedTimezone
	return json.Marshal(values)
}

func (s *Store) withCPASecret(ctx context.Context, agentID string, raw json.RawMessage, managementOverride string) (json.RawMessage, error) {
	installed, err := s.HasActiveDeployment(ctx, agentID, cpaAppKey)
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, errors.New("center: Keeper requires a successful CPA installation on this Agent")
	}
	cpa, err := s.currentCPACredentials(ctx, agentID)
	if err != nil {
		return nil, err
	}
	values, valuesErr := decodeJSONObject(raw, "center: CPA management key is unavailable")
	if valuesErr != nil {
		return nil, errors.New("center: CPA management key is unavailable")
	}
	managementKey := cpa.ManagementKey
	if strings.TrimSpace(managementOverride) != "" {
		managementKey = managementOverride
	}
	encodedManagementKey, err := json.Marshal(managementKey)
	if err != nil {
		return nil, err
	}
	values["cpa_management_key"] = encodedManagementKey
	return json.Marshal(values)
}

func (s *Store) withThreeXUISecrets(ctx context.Context, agentID, operation string, encoded []byte) ([]byte, *OneTimeCredentials, error) {
	values := map[string]string{}
	if len(encoded) != 0 && json.Unmarshal(encoded, &values) != nil {
		return nil, nil, errors.New("center: invalid 3x-ui secret configuration")
	}
	if operation == "upgrade" || operation == "configure" {
		var deploymentID, secretID string
		err := s.db.QueryRowContext(ctx, `SELECT id, secret_id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade', 'configure') AND secret_id IS NOT NULL ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, threeXUIAppKey).Scan(&deploymentID, &secretID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, errors.New("center: previous 3x-ui credentials were not found")
		}
		if err != nil {
			return nil, nil, err
		}
		previous, err := s.getSecret(ctx, secretID, "deployment:"+deploymentID)
		if err != nil {
			return nil, nil, err
		}
		if json.Unmarshal(previous, &values) != nil || values["username"] == "" || values["password"] == "" {
			return nil, nil, errors.New("center: stored 3x-ui credentials are invalid")
		}
		result, err := json.Marshal(values)
		return result, nil, err
	}
	usernameToken, err := randomToken(6)
	if err != nil {
		return nil, nil, err
	}
	password, err := randomToken(24)
	if err != nil {
		return nil, nil, err
	}
	values["username"] = "vastora-" + strings.ToLower(usernameToken)
	values["password"] = password
	result, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	return result, &OneTimeCredentials{Username: values["username"], Password: values["password"]}, nil
}

func (s *Store) storeApplicationSecrets(ctx context.Context, tx *sql.Tx, deploymentID, applicationID string, generated map[string]string, now time.Time) error {
	var secretID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT secret_id FROM deployments WHERE id = ?`, deploymentID).Scan(&secretID); err != nil {
		return err
	}
	values := map[string]string{}
	if secretID.Valid {
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, secretID.String).Scan(&sealed); err != nil {
			return err
		}
		plain, err := secret.Open(s.key, sealed, []byte("deployment:"+deploymentID))
		if err != nil {
			return err
		}
		if json.Unmarshal(plain, &values) != nil {
			return errors.New("center: stored application secrets are invalid")
		}
	}
	for key, value := range generated {
		values[key] = value
	}
	if len(values) == 0 {
		return nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	applicationSecretID, err := s.putSecret(ctx, tx, encoded, "application:"+applicationID)
	if err != nil {
		return err
	}
	var previousApplicationSecretID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT secret_id FROM application_secrets WHERE application_id = ?`, applicationID).Scan(&previousApplicationSecretID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_secrets(application_id, secret_id, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET secret_id = excluded.secret_id, updated_at = excluded.updated_at`, applicationID, applicationSecretID, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if previousApplicationSecretID.Valid && previousApplicationSecretID.String != applicationSecretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousApplicationSecretID.String); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDeploymentConfig(manifest catalog.AppManifest, raw json.RawMessage) ([]byte, json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(manifest.Config))
	if len(raw) > 0 {
		decoded, err := decodeJSONObject(raw, "center: deployment configuration must be a JSON object")
		if err != nil {
			return nil, nil, err
		}
		values = decoded
	}
	fields := make(map[string]catalog.ConfigField, len(manifest.Config))
	for _, field := range manifest.Config {
		fields[field.Key] = field
		if _, exists := values[field.Key]; !exists && field.Default != nil {
			values[field.Key] = *field.Default
		}
	}
	for key := range values {
		if _, exists := fields[key]; !exists {
			return nil, nil, fmt.Errorf("center: unknown configuration field %q", key)
		}
	}
	for _, field := range manifest.Config {
		value, exists := values[field.Key]
		if !exists {
			if field.Required {
				return nil, nil, fmt.Errorf("center: configuration field %q is required", field.Key)
			}
			continue
		}
		normalized, err := normalizeConfigValue(field, value)
		if err != nil {
			return nil, nil, err
		}
		values[field.Key] = normalized
	}
	configuration := make(map[string]json.RawMessage, len(values))
	secrets := make(map[string]json.RawMessage)
	for key, value := range values {
		if fields[key].Secret {
			secrets[key] = value
			continue
		}
		configuration[key] = value
	}
	configJSON, err := json.Marshal(configuration)
	if err != nil {
		return nil, nil, fmt.Errorf("center: encode deployment configuration: %w", err)
	}
	secretJSON, err := json.Marshal(secrets)
	if err != nil {
		return nil, nil, fmt.Errorf("center: encode deployment secrets: %w", err)
	}
	return configJSON, secretJSON, nil
}

func normalizeConfigValue(field catalog.ConfigField, raw json.RawMessage) (json.RawMessage, error) {
	switch field.Type {
	case "string":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("center: configuration field %q must be a string", field.Key)
		}
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("center: configuration field %q must be a boolean", field.Key)
		}
	case "integer":
		normalized, err := catalog.NormalizePortableInteger(raw)
		if err != nil {
			return nil, fmt.Errorf("center: configuration field %q must be a portable integer: %w", field.Key, err)
		}
		return normalized, nil
	default:
		return nil, fmt.Errorf("center: unsupported configuration field %q", field.Key)
	}
	return raw, nil
}
