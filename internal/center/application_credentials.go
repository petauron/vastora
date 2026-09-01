package center

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ApplicationCredentialRotation struct {
	ID                 string    `json:"id"`
	ApplicationID      string    `json:"applicationId"`
	Target             string    `json:"target"`
	State              string    `json:"state"`
	CPADeploymentID    string    `json:"cpaDeploymentId,omitempty"`
	KeeperDeploymentID string    `json:"keeperDeploymentId,omitempty"`
	LastError          string    `json:"lastError,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type credentialRotationRecord struct {
	ApplicationCredentialRotation
	AdminID          string
	AgentID          string
	OperationKeyHash []byte
	RequestHash      []byte
	SecretID         sql.NullString
}

func applicationCredentialRotationHash(applicationID, target string) []byte {
	digest := sha256.Sum256([]byte(strings.TrimSpace(applicationID) + "\x00" + strings.TrimSpace(target)))
	return digest[:]
}

func scanCredentialRotation(scanner interface{ Scan(...any) error }, value *credentialRotationRecord) error {
	var createdAt, updatedAt string
	if err := scanner.Scan(&value.ID, &value.ApplicationID, &value.AdminID, &value.AgentID, &value.OperationKeyHash, &value.RequestHash, &value.Target, &value.SecretID, &value.CPADeploymentID, &value.KeeperDeploymentID, &value.State, &value.LastError, &createdAt, &updatedAt); err != nil {
		return err
	}
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return nil
}

const credentialRotationColumns = `rotation.id, rotation.application_id, rotation.admin_id, application.node_id, rotation.operation_key_hash, rotation.request_hash, rotation.target, rotation.secret_id, rotation.cpa_deployment_id, rotation.keeper_deployment_id, rotation.state, rotation.last_error, rotation.created_at, rotation.updated_at`

func (s *Store) credentialRotationByOperation(ctx context.Context, adminID string, operationKeyHash []byte) (credentialRotationRecord, bool, error) {
	var value credentialRotationRecord
	err := scanCredentialRotation(s.db.QueryRowContext(ctx, `SELECT `+credentialRotationColumns+` FROM application_credential_rotations rotation JOIN applications application ON application.id = rotation.application_id WHERE rotation.admin_id = ? AND rotation.operation_key_hash = ?`, adminID, operationKeyHash), &value)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, fmt.Errorf("center: read application credential rotation: %w", err)
	}
	return value, true, nil
}

func (s *Store) credentialRotationByDeployment(ctx context.Context, deploymentID string) (credentialRotationRecord, bool, error) {
	var value credentialRotationRecord
	err := scanCredentialRotation(s.db.QueryRowContext(ctx, `SELECT `+credentialRotationColumns+` FROM application_credential_rotations rotation JOIN applications application ON application.id = rotation.application_id WHERE rotation.cpa_deployment_id = ? OR rotation.keeper_deployment_id = ?`, deploymentID, deploymentID), &value)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, fmt.Errorf("center: read application credential rotation: %w", err)
	}
	return value, true, nil
}

func (s *Store) RotateApplicationCredentials(ctx context.Context, applicationID, adminID, currentPassword, operationKey, target string, confirm bool) (ApplicationCredentialRotation, error) {
	return s.rotateApplicationCredentials(ctx, applicationID, adminID, currentPassword, operationKey, target, confirm, false)
}

func (s *Store) RotateApplicationCredentialsFromApprovedProposal(ctx context.Context, applicationID, adminID, operationKey, target string) (ApplicationCredentialRotation, error) {
	return s.rotateApplicationCredentials(ctx, applicationID, adminID, "", operationKey, target, true, true)
}

func (s *Store) rotateApplicationCredentials(ctx context.Context, applicationID, adminID, currentPassword, operationKey, target string, confirm, approvalGranted bool) (ApplicationCredentialRotation, error) {
	s.credentialRotationMu.Lock()
	defer s.credentialRotationMu.Unlock()

	applicationID = strings.TrimSpace(applicationID)
	target = strings.TrimSpace(target)
	var agentID, appKey, status string
	if err := s.db.QueryRowContext(ctx, `SELECT node_id, app_key, status FROM applications WHERE id = ?`, applicationID).Scan(&agentID, &appKey, &status); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCredentialRotation{}, errors.New("center: application not found")
	} else if err != nil {
		return ApplicationCredentialRotation{}, fmt.Errorf("center: read credential application: %w", err)
	}
	recordRejectedAttempt := func(message string) {
		_ = s.recordStandaloneTaskEvent(ctx, applicationID, agentID, "security.credentials.rotate", 1, "failed", "CPA "+target+" credential rotation rejected for administrator "+adminID+": "+message)
	}
	if appKey != cpaAppKey || status == "stopped" {
		recordRejectedAttempt("application unavailable")
		return ApplicationCredentialRotation{}, errors.New("center: CPA credentials are unavailable for this application")
	}
	if target != "management" && target != "client" {
		recordRejectedAttempt("invalid target")
		return ApplicationCredentialRotation{}, errors.New("center: credential rotation target must be management or client")
	}
	if !confirm {
		recordRejectedAttempt("confirmation missing")
		return ApplicationCredentialRotation{}, errors.New("center: credential rotation requires explicit confirmation")
	}
	if !approvalGranted {
		if err := s.ReauthenticateAdmin(ctx, adminID, currentPassword); err != nil {
			recordRejectedAttempt("administrator reauthentication failed")
			return ApplicationCredentialRotation{}, err
		}
	}
	adminID, operationKeyHash, err := normalizeSecretOperation(adminID, operationKey)
	if err != nil {
		recordRejectedAttempt("invalid idempotency key")
		return ApplicationCredentialRotation{}, err
	}
	requestHash := applicationCredentialRotationHash(applicationID, target)
	if existing, found, readErr := s.credentialRotationByOperation(ctx, adminID, operationKeyHash); readErr != nil {
		return ApplicationCredentialRotation{}, readErr
	} else if found {
		if existing.ApplicationID != applicationID || subtle.ConstantTimeCompare(existing.RequestHash, requestHash) != 1 {
			return ApplicationCredentialRotation{}, errors.New("center: Idempotency-Key was already used for a different credential rotation")
		}
		return s.resumeCredentialRotation(ctx, existing, true)
	}
	var activeRotations int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_credential_rotations WHERE application_id = ? AND state IN ('preparing', 'pending', 'action_required')`, applicationID).Scan(&activeRotations); err != nil {
		return ApplicationCredentialRotation{}, fmt.Errorf("center: inspect active credential rotations: %w", err)
	}
	if activeRotations != 0 {
		recordRejectedAttempt("another rotation is active")
		return ApplicationCredentialRotation{}, errors.New("center: another CPA credential rotation is still active")
	}

	credentials, err := s.currentCPACredentials(ctx, agentID)
	if err != nil {
		recordRejectedAttempt("current credentials unavailable")
		return ApplicationCredentialRotation{}, err
	}
	replacement, err := randomToken(32)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	if target == "management" {
		credentials.ManagementKey = replacement
	} else {
		credentials.APIKey = replacement
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	rotationID, err := randomToken(18)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	cpaDeploymentID, err := randomToken(18)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	keeperDeploymentID := ""
	var keeperApplicationID string
	if target == "management" {
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM applications WHERE node_id = ? AND app_key = 'vastora-official/keeper' AND status <> 'stopped'`, agentID).Scan(&keeperApplicationID); err == nil {
			keeperDeploymentID, err = randomToken(18)
			if err != nil {
				return ApplicationCredentialRotation{}, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return ApplicationCredentialRotation{}, fmt.Errorf("center: inspect CPA Keeper dependency: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, encoded, "credential-rotation:"+rotationID)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_credential_rotations(id, application_id, admin_id, operation_key_hash, request_hash, target, secret_id, cpa_deployment_id, keeper_deployment_id, state, last_error, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'preparing', '', ?, ?)`, rotationID, applicationID, adminID, operationKeyHash, requestHash, target, secretID, cpaDeploymentID, keeperDeploymentID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return ApplicationCredentialRotation{}, fmt.Errorf("center: create application credential rotation: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, applicationID, agentID, "security.credentials.rotate", 1, "queued", "CPA "+target+" credential rotation requested by administrator "+adminID); err != nil {
		return ApplicationCredentialRotation{}, fmt.Errorf("center: record credential rotation audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCredentialRotation{}, err
	}
	created, _, err := s.credentialRotationByOperation(ctx, adminID, operationKeyHash)
	if err != nil {
		return ApplicationCredentialRotation{}, err
	}
	return s.resumeCredentialRotation(ctx, created, false)
}

func (s *Store) credentialRotationSecrets(ctx context.Context, rotation credentialRotationRecord) (cpaCredentialValues, error) {
	if !rotation.SecretID.Valid {
		return cpaCredentialValues{}, errors.New("center: credential rotation secret is no longer available")
	}
	plain, err := s.getSecret(ctx, rotation.SecretID.String, "credential-rotation:"+rotation.ID)
	if err != nil {
		return cpaCredentialValues{}, err
	}
	return decodeCPACredentialValues(plain)
}

func (s *Store) deploymentState(ctx context.Context, id string) (string, bool, error) {
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM deployments WHERE id = ?`, id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	return state, true, nil
}

func (s *Store) resumeCredentialRotation(ctx context.Context, rotation credentialRotationRecord, retry bool) (ApplicationCredentialRotation, error) {
	if rotation.State == "succeeded" {
		return rotation.ApplicationCredentialRotation, nil
	}
	credentials, err := s.credentialRotationSecrets(ctx, rotation)
	if err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	cpaState, exists, err := s.deploymentState(ctx, rotation.CPADeploymentID)
	if err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	if exists && cpaState == "failed" && retry {
		rotation.CPADeploymentID, err = randomToken(18)
		if err != nil {
			return rotation.ApplicationCredentialRotation, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE application_credential_rotations SET cpa_deployment_id = ?, state = 'preparing', last_error = '', updated_at = ? WHERE id = ?`, rotation.CPADeploymentID, s.now().UTC().Format(time.RFC3339Nano), rotation.ID); err != nil {
			return rotation.ApplicationCredentialRotation, err
		}
		exists = false
	}
	if !exists {
		if _, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: rotation.AgentID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{}`), InternalDeploymentID: rotation.CPADeploymentID, CPACredentials: &credentials, CredentialRotation: true}); err != nil {
			return s.finishCredentialRotation(ctx, rotation, "failed", controlCredentialRotationError(err))
		}
		cpaState = "pending"
		if _, err := s.db.ExecContext(ctx, `UPDATE application_credential_rotations SET state = 'pending', updated_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339Nano), rotation.ID); err != nil {
			return rotation.ApplicationCredentialRotation, err
		}
	}
	if cpaState == "pending" || cpaState == "running" {
		return s.refreshCredentialRotation(ctx, rotation.ID)
	}
	if cpaState == "failed" {
		return s.finishCredentialRotation(ctx, rotation, "failed", "CPA rejected the rotated credential")
	}
	if rotation.KeeperDeploymentID == "" {
		return s.finishCredentialRotation(ctx, rotation, "succeeded", "")
	}
	keeperState, keeperExists, err := s.deploymentState(ctx, rotation.KeeperDeploymentID)
	if err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	if keeperExists && keeperState == "failed" && retry {
		rotation.KeeperDeploymentID, err = randomToken(18)
		if err != nil {
			return rotation.ApplicationCredentialRotation, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE application_credential_rotations SET keeper_deployment_id = ?, state = 'pending', last_error = '', updated_at = ? WHERE id = ?`, rotation.KeeperDeploymentID, s.now().UTC().Format(time.RFC3339Nano), rotation.ID); err != nil {
			return rotation.ApplicationCredentialRotation, err
		}
		keeperExists = false
	}
	if !keeperExists {
		if _, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: rotation.AgentID, AppKey: "vastora-official/keeper", Operation: "configure", Config: json.RawMessage(`{}`), InternalDeploymentID: rotation.KeeperDeploymentID, CPAManagementKey: credentials.ManagementKey, CredentialRotation: true}); err != nil {
			return s.finishCredentialRotation(ctx, rotation, "action_required", controlCredentialRotationError(err))
		}
		keeperState = "pending"
	}
	if keeperState == "pending" || keeperState == "running" {
		return s.refreshCredentialRotation(ctx, rotation.ID)
	}
	if keeperState == "failed" {
		return s.finishCredentialRotation(ctx, rotation, "action_required", "Keeper rejected the rotated CPA management credential")
	}
	return s.finishCredentialRotation(ctx, rotation, "succeeded", "")
}

func controlCredentialRotationError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}

func (s *Store) refreshCredentialRotation(ctx context.Context, id string) (ApplicationCredentialRotation, error) {
	var value ApplicationCredentialRotation
	var createdAt, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT id, application_id, target, state, cpa_deployment_id, keeper_deployment_id, last_error, created_at, updated_at FROM application_credential_rotations WHERE id = ?`, id).Scan(&value.ID, &value.ApplicationID, &value.Target, &value.State, &value.CPADeploymentID, &value.KeeperDeploymentID, &value.LastError, &createdAt, &updatedAt); err != nil {
		return value, err
	}
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return value, nil
}

func (s *Store) ApplicationCredentialRotation(ctx context.Context, applicationID, rotationID string) (ApplicationCredentialRotation, error) {
	value, err := s.refreshCredentialRotation(ctx, strings.TrimSpace(rotationID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && value.ApplicationID != strings.TrimSpace(applicationID) {
		return ApplicationCredentialRotation{}, errors.New("center: application credential rotation not found")
	}
	if err != nil {
		return ApplicationCredentialRotation{}, fmt.Errorf("center: read application credential rotation: %w", err)
	}
	return value, nil
}

func (s *Store) finishCredentialRotation(ctx context.Context, rotation credentialRotationRecord, state, message string) (ApplicationCredentialRotation, error) {
	if state != "succeeded" && state != "failed" && state != "action_required" {
		return rotation.ApplicationCredentialRotation, errors.New("center: invalid credential rotation state")
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE application_credential_rotations SET state = ?, last_error = ?, secret_id = CASE WHEN ? = 'succeeded' THEN NULL ELSE secret_id END, updated_at = ? WHERE id = ? AND state <> ?`, state, message, state, now.Format(time.RFC3339Nano), rotation.ID, state)
	if err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	changed, _ := result.RowsAffected()
	if changed != 0 {
		if state == "succeeded" && rotation.SecretID.Valid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, rotation.SecretID.String); err != nil {
				return rotation.ApplicationCredentialRotation, err
			}
		}
		event := "succeeded"
		if state != "succeeded" {
			event = "failed"
		}
		if auditErr := s.recordTaskEvent(ctx, tx, rotation.ApplicationID, rotation.AgentID, "security.credentials.rotate", 1, event, "CPA "+rotation.Target+" credential rotation "+state+" for administrator "+rotation.AdminID); auditErr != nil {
			return rotation.ApplicationCredentialRotation, auditErr
		}
	}
	if err := tx.Commit(); err != nil {
		return rotation.ApplicationCredentialRotation, err
	}
	return s.refreshCredentialRotation(ctx, rotation.ID)
}

func (s *Store) resumeCredentialRotationForDeployment(ctx context.Context, deploymentID string) {
	s.credentialRotationMu.Lock()
	defer s.credentialRotationMu.Unlock()
	rotation, found, err := s.credentialRotationByDeployment(ctx, deploymentID)
	if err != nil || !found {
		return
	}
	_, _ = s.resumeCredentialRotation(ctx, rotation, false)
}
