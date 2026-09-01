package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

type InstallOperation struct {
	OperationID      string
	CenterURL        string
	CAFingerprint    string
	CACertificatePEM string
	ReplaceExisting  bool
	Phase            string
	LastError        string
	Enrollment       Enrollment
	Token            string
	PrivateKey       []byte
}

func (s *Store) BeginEnrollmentOperation(ctx context.Context, centerURL, token, caFingerprint, caCertificatePEM string, replace bool) (InstallOperation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return InstallOperation{}, errors.New("agent: enrollment token is required")
	}
	tokenDigest := sha256.Sum256([]byte(token))
	if operation, exists, err := s.installOperation(ctx, true); err != nil {
		return InstallOperation{}, err
	} else if exists {
		if operation.CenterURL != centerURL || operation.CAFingerprint != caFingerprint || operation.CACertificatePEM != caCertificatePEM || operation.ReplaceExisting != replace {
			return InstallOperation{}, errors.New("agent: another installation operation is pending; rerun it with the original Center options")
		}
		var storedTokenHash []byte
		if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM agent_install_operations WHERE id = 1`).Scan(&storedTokenHash); err != nil {
			return InstallOperation{}, err
		}
		if !bytes.Equal(storedTokenHash, tokenDigest[:]) {
			return InstallOperation{}, errors.New("agent: another installation operation is pending; use its original enrollment token")
		}
		if operation.Phase == "enrollment_pending" {
			operation.Token = token
		}
		return operation, nil
	}
	operationID, err := randomEnrollmentOperationID()
	if err != nil {
		return InstallOperation{}, err
	}
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		return InstallOperation{}, err
	}
	sealedToken, err := secret.Seal(s.key, []byte(token), []byte("agent-install-token:"+operationID))
	if err != nil {
		return InstallOperation{}, fmt.Errorf("agent: encrypt enrollment token: %w", err)
	}
	sealedPrivateKey, err := secret.Seal(s.key, privateKey, []byte("agent-install-key:"+operationID))
	if err != nil {
		return InstallOperation{}, fmt.Errorf("agent: encrypt enrollment identity: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstallOperation{}, err
	}
	defer tx.Rollback()
	var previous Connection
	previousSealedCredential := []byte{}
	previousSealedPrivateKey := []byte{}
	connectionErr := tx.QueryRowContext(ctx, `SELECT agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem
		FROM control_plane_connection WHERE id = 1`).Scan(&previous.AgentID, &previous.Name, &previous.CenterURL, &previousSealedCredential, &previousSealedPrivateKey, &previous.CAFingerprint, &previous.CACertificatePEM)
	if connectionErr != nil && !errors.Is(connectionErr, sql.ErrNoRows) {
		return InstallOperation{}, fmt.Errorf("agent: inspect existing Center enrollment: %w", connectionErr)
	}
	hasConnection := connectionErr == nil
	if hasConnection && !replace {
		return InstallOperation{}, errors.New("agent: already enrolled; use agent update or agent configure")
	} else if !hasConnection && replace {
		return InstallOperation{}, errors.New("agent: cannot replace a Center enrollment because no existing enrollment was found")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_install_operations(
		id, operation_id, center_url, token_hash, sealed_token, sealed_private_key, ca_fingerprint, ca_certificate_pem,
		replace_existing, phase, previous_agent_id, previous_name, previous_center_url,
		previous_sealed_credential, previous_sealed_private_key, previous_ca_fingerprint, previous_ca_certificate_pem, created_at, updated_at
	) VALUES(1, ?, ?, ?, ?, ?, ?, ?, ?, 'enrollment_pending', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, operationID, centerURL, tokenDigest[:], sealedToken, sealedPrivateKey, caFingerprint, caCertificatePEM, replace,
		previous.AgentID, previous.Name, previous.CenterURL, previousSealedCredential, previousSealedPrivateKey, previous.CAFingerprint, previous.CACertificatePEM, now, now); err != nil {
		return InstallOperation{}, fmt.Errorf("agent: persist enrollment operation before contacting Center: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InstallOperation{}, fmt.Errorf("agent: commit enrollment operation: %w", err)
	}
	return InstallOperation{OperationID: operationID, CenterURL: centerURL, CAFingerprint: caFingerprint, CACertificatePEM: caCertificatePEM, ReplaceExisting: replace, Phase: "enrollment_pending", Token: token, PrivateKey: privateKey}, nil
}

func (s *Store) installOperation(ctx context.Context, includeSecrets bool) (InstallOperation, bool, error) {
	var operation InstallOperation
	var replace bool
	var sealedToken, sealedPrivateKey, rolesJSON, capabilitiesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation_id, center_url, sealed_token, sealed_private_key, ca_fingerprint, ca_certificate_pem,
		replace_existing, phase, agent_id, name, roles_json, capabilities_json, last_error
		FROM agent_install_operations WHERE id = 1`).Scan(
		&operation.OperationID, &operation.CenterURL, &sealedToken, &sealedPrivateKey, &operation.CAFingerprint, &operation.CACertificatePEM,
		&replace, &operation.Phase, &operation.Enrollment.ID, &operation.Enrollment.Name, &rolesJSON, &capabilitiesJSON, &operation.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return InstallOperation{}, false, nil
	}
	if err != nil {
		return InstallOperation{}, false, fmt.Errorf("agent: read installation operation: %w", err)
	}
	operation.ReplaceExisting = replace
	if !validInstallPhase(operation.Phase) {
		return InstallOperation{}, false, errors.New("agent: stored installation phase is invalid")
	}
	if operation.Phase != "enrollment_pending" {
		if json.Unmarshal(rolesJSON, &operation.Enrollment.Roles) != nil || json.Unmarshal(capabilitiesJSON, &operation.Enrollment.Capabilities) != nil || operation.Enrollment.ID == "" || operation.Enrollment.Name == "" {
			return InstallOperation{}, false, errors.New("agent: stored installation profile is invalid")
		}
	}
	if includeSecrets && operation.Phase == "enrollment_pending" {
		token, err := secret.Open(s.key, sealedToken, []byte("agent-install-token:"+operation.OperationID))
		if err != nil {
			return InstallOperation{}, false, fmt.Errorf("agent: decrypt enrollment token: %w", err)
		}
		privateKey, err := secret.Open(s.key, sealedPrivateKey, []byte("agent-install-key:"+operation.OperationID))
		if err != nil {
			return InstallOperation{}, false, fmt.Errorf("agent: decrypt enrollment identity: %w", err)
		}
		if _, err := controlplane.PublicKey(privateKey); err != nil {
			return InstallOperation{}, false, errors.New("agent: stored enrollment identity is invalid")
		}
		operation.Token = string(token)
		operation.PrivateKey = privateKey
	}
	return operation, true, nil
}

func (s *Store) InstallOperation(ctx context.Context) (InstallOperation, bool, error) {
	return s.installOperation(ctx, false)
}

func (s *Store) CompleteEnrollmentOperation(ctx context.Context, operation InstallOperation, response Enrollment) error {
	if operation.Phase != "enrollment_pending" || response.ID == "" || response.Credential == "" || response.Name == "" || len(response.Roles) == 0 {
		return errors.New("agent: incomplete enrollment operation")
	}
	connection := Connection{AgentID: response.ID, Name: response.Name, CenterURL: operation.CenterURL, Credential: response.Credential, PrivateKey: operation.PrivateKey, CAFingerprint: operation.CAFingerprint, CACertificatePEM: operation.CACertificatePEM}
	sealedCredential, sealedPrivateKey, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	rolesJSON, err := json.Marshal(response.Roles)
	if err != nil {
		return err
	}
	capabilitiesJSON, err := json.Marshal(response.Capabilities)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if operation.ReplaceExisting {
		if _, err := tx.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem)
			VALUES(1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET agent_id = excluded.agent_id, name = excluded.name, center_url = excluded.center_url,
			sealed_credential = excluded.sealed_credential, sealed_private_key = excluded.sealed_private_key, ca_fingerprint = excluded.ca_fingerprint, ca_certificate_pem = excluded.ca_certificate_pem`,
			connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint), strings.TrimSpace(connection.CACertificatePEM)); err != nil {
			return fmt.Errorf("agent: replace Center connection: %w", err)
		}
	} else {
		result, err := tx.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem)
			VALUES(1, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint), strings.TrimSpace(connection.CACertificatePEM))
		if err != nil {
			return fmt.Errorf("agent: save Center connection: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("agent: already enrolled; installation operation did not own the existing connection")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_install_operations SET phase = 'enrolled', agent_id = ?, name = ?, roles_json = ?, capabilities_json = ?,
		sealed_token = X'', sealed_private_key = X'', last_error = '', updated_at = ? WHERE id = 1 AND operation_id = ? AND phase = 'enrollment_pending'`,
		response.ID, response.Name, rolesJSON, capabilitiesJSON, s.now().UTC().Format(time.RFC3339Nano), operation.OperationID)
	if err != nil {
		return fmt.Errorf("agent: persist completed enrollment operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: enrollment operation changed before local commit")
	}
	return tx.Commit()
}

func (s *Store) EnrollmentForInstallOperation(ctx context.Context) (Enrollment, error) {
	operation, exists, err := s.installOperation(ctx, false)
	if err != nil || !exists {
		return Enrollment{}, errors.Join(errors.New("agent: installation operation is missing"), err)
	}
	if operation.Phase == "enrollment_pending" {
		return Enrollment{}, errors.New("agent: enrollment operation is not complete")
	}
	connection, err := s.Connection(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	if connection.AgentID != operation.Enrollment.ID || connection.CenterURL != operation.CenterURL {
		return Enrollment{}, errors.New("agent: stored installation operation does not match the Center connection")
	}
	operation.Enrollment.Credential = connection.Credential
	return operation.Enrollment, nil
}

func (s *Store) AdvanceInstallOperation(ctx context.Context, expected, next string) error {
	if !validInstallPhase(expected) || !validInstallPhase(next) || installPhaseIndex(next) != installPhaseIndex(expected)+1 {
		return errors.New("agent: invalid installation phase transition")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_install_operations SET phase = ?, last_error = '', updated_at = ? WHERE id = 1 AND phase = ?`, next, s.now().UTC().Format(time.RFC3339Nano), expected)
	if err != nil {
		return fmt.Errorf("agent: advance installation phase: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: installation phase changed while applying")
	}
	return nil
}

func (s *Store) RecordInstallOperationError(ctx context.Context, cause error) {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2048 {
		message = message[:2048]
	}
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE agent_install_operations SET last_error = ?, updated_at = ? WHERE id = 1`, message, s.now().UTC().Format(time.RFC3339Nano))
}

func (s *Store) CompleteInstallOperation(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_install_operations WHERE id = 1 AND phase = 'healthy'`)
	if err != nil {
		return fmt.Errorf("agent: complete installation operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: installation operation is not ready to complete")
	}
	return nil
}

// RollbackInstallOperation restores the previous Center identity after a
// replacement fails. Fresh installations remove only the connection created
// by their own operation. Host files and Tailscale profiles are restored by
// the host-switch journal in cmd/vastora.
func (s *Store) RollbackInstallOperation(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var operationID, phase, agentID string
	var replace bool
	var previous Connection
	var previousSealedCredential, previousSealedPrivateKey []byte
	err = tx.QueryRowContext(ctx, `SELECT operation_id, phase, agent_id, replace_existing,
		previous_agent_id, previous_name, previous_center_url, previous_sealed_credential,
		previous_sealed_private_key, previous_ca_fingerprint, previous_ca_certificate_pem
		FROM agent_install_operations WHERE id = 1`).Scan(&operationID, &phase, &agentID, &replace,
		&previous.AgentID, &previous.Name, &previous.CenterURL, &previousSealedCredential,
		&previousSealedPrivateKey, &previous.CAFingerprint, &previous.CACertificatePEM)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: read installation rollback state: %w", err)
	}
	if replace {
		if previous.AgentID == "" || previous.Name == "" || previous.CenterURL == "" || len(previousSealedCredential) == 0 || len(previousSealedPrivateKey) == 0 {
			return errors.New("agent: previous Center enrollment snapshot is incomplete; refusing destructive rollback")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem)
			VALUES(1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET agent_id = excluded.agent_id, name = excluded.name, center_url = excluded.center_url,
			sealed_credential = excluded.sealed_credential, sealed_private_key = excluded.sealed_private_key, ca_fingerprint = excluded.ca_fingerprint, ca_certificate_pem = excluded.ca_certificate_pem`,
			previous.AgentID, previous.Name, previous.CenterURL, previousSealedCredential, previousSealedPrivateKey, previous.CAFingerprint, previous.CACertificatePEM); err != nil {
			return fmt.Errorf("agent: restore previous Center enrollment: %w", err)
		}
	} else if phase != "enrollment_pending" {
		result, err := tx.ExecContext(ctx, `DELETE FROM control_plane_connection WHERE id = 1 AND agent_id = ?`, agentID)
		if err != nil {
			return fmt.Errorf("agent: remove incomplete Center enrollment: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("agent: incomplete installation no longer owns the Center enrollment")
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM agent_install_operations WHERE id = 1 AND operation_id = ?`, operationID)
	if err != nil {
		return fmt.Errorf("agent: clear installation rollback state: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: installation operation changed during rollback")
	}
	return tx.Commit()
}

// CompleteEnrollmentOnlyOperation finishes the explicit `agent enroll`
// workflow, which intentionally does not install a host service. The durable
// operation remains until this point so a lost response or local commit can be
// replayed, but must not make a successful enrollment look like a partial
// systemd installation afterward.
func (s *Store) CompleteEnrollmentOnlyOperation(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_install_operations WHERE id = 1 AND phase = 'enrolled'`)
	if err != nil {
		return fmt.Errorf("agent: complete enrollment-only operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: enrollment-only operation is not ready to complete")
	}
	return nil
}

func validInstallPhase(value string) bool {
	return installPhaseIndex(value) >= 0
}

func installPhaseIndex(value string) int {
	for index, phase := range []string{"enrollment_pending", "enrolled", "unit_written", "reloaded", "enabled", "started", "healthy"} {
		if value == phase {
			return index
		}
	}
	return -1
}

func randomEnrollmentOperationID() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent: generate enrollment operation ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
