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
	values := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("center: deployment configuration must be a JSON object")
	}
	for _, key := range keys {
		delete(values, key)
	}
	return json.Marshal(values)
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
	merged := map[string]json.RawMessage{}
	if json.Unmarshal(configJSON, &merged) != nil {
		return nil, errors.New("center: stored deployment configuration is invalid")
	}
	if secretID.Valid {
		secretJSON, err := s.getSecret(ctx, secretID.String, "deployment:"+deploymentID)
		if err != nil {
			return nil, err
		}
		var secretValues map[string]json.RawMessage
		if json.Unmarshal(secretJSON, &secretValues) != nil {
			return nil, errors.New("center: stored deployment secrets are invalid")
		}
		for key, value := range secretValues {
			merged[key] = value
		}
	}
	if len(updates) != 0 {
		var changed map[string]json.RawMessage
		if json.Unmarshal(updates, &changed) != nil {
			return nil, errors.New("center: deployment configuration must be a JSON object")
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

func (s *Store) withCPASecret(ctx context.Context, agentID string, raw json.RawMessage) (json.RawMessage, error) {
	installed, err := s.HasActiveDeployment(ctx, agentID, cpaAppKey)
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, errors.New("center: Keeper requires a successful CPA installation on this Agent")
	}
	var deploymentID, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT id, secret_id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade', 'configure') AND secret_id IS NOT NULL ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, cpaAppKey).Scan(&deploymentID, &secretID); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: Keeper requires a successful CPA installation on this Agent")
	} else if err != nil {
		return nil, fmt.Errorf("center: read CPA deployment: %w", err)
	}
	cpaSecrets, err := s.getSecret(ctx, secretID, "deployment:"+deploymentID)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	var cpa map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || json.Unmarshal(cpaSecrets, &cpa) != nil || cpa["management_key"] == nil {
		return nil, errors.New("center: CPA management key is unavailable")
	}
	values["cpa_management_key"] = cpa["management_key"]
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
	_, err = tx.ExecContext(ctx, `INSERT INTO application_secrets(application_id, secret_id, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET secret_id = excluded.secret_id, updated_at = excluded.updated_at`, applicationID, applicationSecretID, now.Format(time.RFC3339Nano))
	return err
}

func normalizeDeploymentConfig(manifest catalog.AppManifest, raw json.RawMessage) ([]byte, json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(manifest.Config))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, nil, errors.New("center: deployment configuration must be a JSON object")
		}
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
		if err := validateConfigValue(field, value); err != nil {
			return nil, nil, err
		}
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

func validateConfigValue(field catalog.ConfigField, raw json.RawMessage) error {
	switch field.Type {
	case "string":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be a string", field.Key)
		}
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be a boolean", field.Key)
		}
	case "integer":
		var value int
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be an integer", field.Key)
		}
	default:
		return fmt.Errorf("center: unsupported configuration field %q", field.Key)
	}
	return nil
}
