package center

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

const (
	deploymentCredentialsDelivery = "deployment_credentials"
	applicationCommandDelivery    = "application_command_result"
)

var secretOperationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{16,128}$`)

type secretDelivery struct {
	ResourceID  string
	RequestHash []byte
	State       string
}

func normalizeSecretOperation(ownerID, operationKey string) (string, []byte, error) {
	ownerID = strings.TrimSpace(ownerID)
	operationKey = strings.TrimSpace(operationKey)
	if ownerID == "" || len(ownerID) > 256 {
		return "", nil, errors.New("center: authenticated secret delivery owner is required")
	}
	if !secretOperationKeyPattern.MatchString(operationKey) {
		return "", nil, errors.New("center: Idempotency-Key must contain 16 to 128 letters, digits, dots, underscores, or hyphens")
	}
	digest := sha256.Sum256([]byte(operationKey))
	return ownerID, digest[:], nil
}

func deploymentSecretRequestHash(request DeploymentRequest) ([]byte, error) {
	var configuration any = map[string]any{}
	if len(request.Config) != 0 {
		if err := json.Unmarshal(request.Config, &configuration); err != nil {
			return nil, errors.New("center: deployment configuration must be valid JSON")
		}
	}
	payload := struct {
		AgentID              string  `json:"agentId"`
		AppKey               string  `json:"appKey"`
		Role                 string  `json:"role"`
		Config               any     `json:"config"`
		Operation            string  `json:"operation"`
		DeleteData           bool    `json:"deleteData"`
		RegistryCredentialID *string `json:"registryCredentialId"`
	}{
		AgentID: request.AgentID, AppKey: request.AppKey, Role: request.Role, Config: configuration,
		Operation: request.Operation, DeleteData: request.DeleteData, RegistryCredentialID: request.RegistryCredentialID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("center: encode deployment operation: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func commandSecretRequestHash(commandID string) []byte {
	digest := sha256.Sum256([]byte(applicationCommandDelivery + ":" + commandID))
	return digest[:]
}

func readSecretDelivery(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind, ownerID string, operationKeyHash []byte) (secretDelivery, bool, error) {
	var delivery secretDelivery
	err := queryer.QueryRowContext(ctx, `SELECT resource_id, request_hash, state FROM secret_deliveries WHERE kind = ? AND owner_id = ? AND operation_key_hash = ?`, kind, ownerID, operationKeyHash).Scan(&delivery.ResourceID, &delivery.RequestHash, &delivery.State)
	if errors.Is(err, sql.ErrNoRows) {
		return delivery, false, nil
	}
	if err != nil {
		return delivery, false, fmt.Errorf("center: read secret delivery: %w", err)
	}
	return delivery, true, nil
}

func insertSecretDelivery(ctx context.Context, tx *sql.Tx, kind, ownerID string, operationKeyHash, requestHash []byte, resourceID string, now time.Time) error {
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_deliveries(kind, owner_id, operation_key_hash, request_hash, resource_id, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, 'pending', ?, ?)`, kind, ownerID, operationKeyHash, requestHash, resourceID, stamp, stamp); err != nil {
		return fmt.Errorf("center: create secret delivery: %w", err)
	}
	return nil
}

func validateSecretDelivery(delivery secretDelivery, resourceID string, requestHash []byte) error {
	if delivery.ResourceID != resourceID || subtle.ConstantTimeCompare(delivery.RequestHash, requestHash) != 1 {
		return errors.New("center: Idempotency-Key was already used for a different operation")
	}
	if delivery.State != "pending" {
		return errors.New("center: one-time result was already acknowledged")
	}
	return nil
}

func (s *Store) replayDeploymentCredentials(ctx context.Context, ownerID string, operationKeyHash, requestHash []byte) (DeploymentView, bool, error) {
	delivery, exists, err := readSecretDelivery(ctx, s.db, deploymentCredentialsDelivery, ownerID, operationKeyHash)
	if err != nil || !exists {
		return DeploymentView{}, exists, err
	}
	if err := validateSecretDelivery(delivery, delivery.ResourceID, requestHash); err != nil {
		return DeploymentView{}, true, err
	}
	deployment, err := s.deploymentByID(ctx, delivery.ResourceID)
	if err != nil {
		return DeploymentView{}, true, err
	}
	credentials, err := s.deploymentCredentials(ctx, s.db, delivery.ResourceID)
	if err != nil {
		return DeploymentView{}, true, err
	}
	deployment.OneTimeCredentials = &credentials
	deployment.OneTimeCredentialsAvailable = true
	return deployment, true, nil
}

func (s *Store) deploymentByID(ctx context.Context, id string) (DeploymentView, error) {
	var value DeploymentView
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, agent_id, app_key, app_version, operation, delete_data, state, reconciliation_required, error, created_at, updated_at, application_id FROM deployments WHERE id = ?`, id).Scan(
		&value.ID, &value.AgentID, &value.AppKey, &value.AppVersion, &value.Operation, &value.DeleteData, &value.State, &value.ReconciliationRequired, &value.Error, &createdAt, &updatedAt, &value.ApplicationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return value, errors.New("center: deployment not found")
	}
	if err != nil {
		return value, fmt.Errorf("center: read deployment: %w", err)
	}
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return value, nil
}

func (s *Store) deploymentCredentials(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, deploymentID string) (OneTimeCredentials, error) {
	var sealed []byte
	if err := queryer.QueryRowContext(ctx, `SELECT secret.sealed FROM deployments deployment JOIN secrets secret ON secret.id = deployment.secret_id WHERE deployment.id = ? AND deployment.app_key = ? AND deployment.operation = 'install'`, deploymentID, threeXUIAppKey).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return OneTimeCredentials{}, errors.New("center: one-time deployment credentials are unavailable")
	} else if err != nil {
		return OneTimeCredentials{}, fmt.Errorf("center: read one-time deployment credentials: %w", err)
	}
	plain, err := secret.Open(s.key, sealed, []byte("deployment:"+deploymentID))
	if err != nil {
		return OneTimeCredentials{}, errors.New("center: one-time deployment credentials are invalid")
	}
	var values map[string]string
	if json.Unmarshal(plain, &values) != nil || strings.TrimSpace(values["username"]) == "" || strings.TrimSpace(values["password"]) == "" {
		return OneTimeCredentials{}, errors.New("center: one-time deployment credentials are invalid")
	}
	return OneTimeCredentials{Username: values["username"], Password: values["password"]}, nil
}

func (s *Store) RevealDeploymentCredentials(ctx context.Context, deploymentID, ownerID, operationKey string) (OneTimeCredentials, error) {
	s.secretDeliveryMu.Lock()
	defer s.secretDeliveryMu.Unlock()
	ownerID, operationKeyHash, err := normalizeSecretOperation(ownerID, operationKey)
	if err != nil {
		return OneTimeCredentials{}, err
	}
	delivery, exists, err := readSecretDelivery(ctx, s.db, deploymentCredentialsDelivery, ownerID, operationKeyHash)
	if err != nil {
		return OneTimeCredentials{}, err
	}
	if !exists || delivery.ResourceID != strings.TrimSpace(deploymentID) || delivery.State != "pending" {
		return OneTimeCredentials{}, errors.New("center: one-time deployment credentials are unavailable")
	}
	return s.deploymentCredentials(ctx, s.db, delivery.ResourceID)
}

func (s *Store) AcknowledgeDeploymentCredentials(ctx context.Context, deploymentID, ownerID, operationKey string) error {
	s.secretDeliveryMu.Lock()
	defer s.secretDeliveryMu.Unlock()
	ownerID, operationKeyHash, err := normalizeSecretOperation(ownerID, operationKey)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE secret_deliveries SET state = 'acknowledged', updated_at = ? WHERE kind = ? AND owner_id = ? AND operation_key_hash = ? AND resource_id = ? AND state = 'pending'`, s.now().UTC().Format(time.RFC3339Nano), deploymentCredentialsDelivery, ownerID, operationKeyHash, strings.TrimSpace(deploymentID))
	if err != nil {
		return fmt.Errorf("center: acknowledge deployment credentials: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: one-time deployment credentials are unavailable")
	}
	return nil
}

func (s *Store) RevealApplicationCommandResult(ctx context.Context, commandID, ownerID, operationKey string) (string, error) {
	s.secretDeliveryMu.Lock()
	defer s.secretDeliveryMu.Unlock()
	commandID = strings.TrimSpace(commandID)
	ownerID, operationKeyHash, err := normalizeSecretOperation(ownerID, operationKey)
	if err != nil {
		return "", err
	}
	requestHash := commandSecretRequestHash(commandID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	delivery, exists, err := readSecretDelivery(ctx, tx, applicationCommandDelivery, ownerID, operationKeyHash)
	if err != nil {
		return "", err
	}
	if exists {
		if err := validateSecretDelivery(delivery, commandID, requestHash); err != nil {
			return "", err
		}
	} else {
		var existingState string
		err := tx.QueryRowContext(ctx, `SELECT state FROM secret_deliveries WHERE kind = ? AND resource_id = ?`, applicationCommandDelivery, commandID).Scan(&existingState)
		if err == nil {
			return "", errors.New("center: one-time application result is owned by a different delivery operation")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if err := insertSecretDelivery(ctx, tx, applicationCommandDelivery, ownerID, operationKeyHash, requestHash, commandID, s.now()); err != nil {
			return "", err
		}
	}
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT secret.sealed FROM application_commands command JOIN secrets secret ON secret.id = command.result_secret_id WHERE command.id = ? AND command.state = 'succeeded'`, commandID).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: one-time application result is unavailable")
	} else if err != nil {
		return "", err
	}
	plain, err := secret.Open(s.key, sealed, []byte("application-command:"+commandID))
	if err != nil {
		return "", errors.New("center: one-time application result is invalid")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Store) AcknowledgeApplicationCommandResult(ctx context.Context, commandID, ownerID, operationKey string) error {
	s.secretDeliveryMu.Lock()
	defer s.secretDeliveryMu.Unlock()
	commandID = strings.TrimSpace(commandID)
	ownerID, operationKeyHash, err := normalizeSecretOperation(ownerID, operationKey)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delivery, exists, err := readSecretDelivery(ctx, tx, applicationCommandDelivery, ownerID, operationKeyHash)
	if err != nil {
		return err
	}
	if !exists || validateSecretDelivery(delivery, commandID, commandSecretRequestHash(commandID)) != nil {
		return errors.New("center: one-time application result is unavailable")
	}
	var secretID string
	if err := tx.QueryRowContext(ctx, `SELECT result_secret_id FROM application_commands WHERE id = ? AND state = 'succeeded' AND result_secret_id IS NOT NULL`, commandID).Scan(&secretID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: one-time application result is unavailable")
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET result_secret_id = NULL WHERE id = ?`, commandID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE secret_deliveries SET state = 'acknowledged', updated_at = ? WHERE kind = ? AND owner_id = ? AND operation_key_hash = ?`, s.now().UTC().Format(time.RFC3339Nano), applicationCommandDelivery, ownerID, operationKeyHash); err != nil {
		return err
	}
	return tx.Commit()
}
