package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/recovery"
	"github.com/petauron/vastora/internal/secret"
)

// RecordOSReinstallBackup acknowledges only a complete off-host, encrypted
// recovery artifact tied to the authenticated one-use task. The Agent must
// treat a lost acknowledgment as unknown and must not arm a boot or retry.
func (s *Store) RecordOSReinstallBackup(ctx context.Context, agentID, sessionID, executionID string, evidence osreinstall.BackupEvidence) error {
	bad := func() error { return errors.New("center: reinstall backup evidence rejected") }
	if agentID == "" || sessionID == "" || executionID == "" || len(evidence.Ciphertext) == 0 ||
		len(evidence.Ciphertext) > osreinstall.MaxBackupBytes || len(evidence.AccessCiphertext) == 0 || len(evidence.Ciphertext)+len(evidence.AccessCiphertext) > 700<<10 || evidence.Digest != recovery.Digest(evidence.Ciphertext) {
		return bad()
	}
	var taskID, raw, created string
	var sealedPassword []byte
	now := s.now().UTC().Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx, `SELECT q.id,q.input_json,q.sealed_password,q.created_at
		FROM os_reinstall_tasks q JOIN task_executions e ON e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt
		JOIN agent_execution_sessions session ON session.agent_id=e.agent_id AND session.session_id=e.session_id
		JOIN agents a ON a.id=q.agent_id
		WHERE e.id=? AND e.agent_id=? AND e.session_id=? AND e.kind=? AND e.state='running'
		AND e.phase='apply' AND e.disposition='' AND e.expires_at>?
		AND q.state='running' AND q.attempt=1 AND q.lease_expires_at>?
		AND a.status='active' AND a.credential_revoked_at=''`, executionID, agentID, sessionID, osreinstall.TaskKind, now, now).
		Scan(&taskID, &raw, &sealedPassword, &created)
	if err != nil {
		return bad()
	}
	password, err := secret.Open(s.key, sealedPassword, []byte("os-reinstall-password:"+taskID))
	if err != nil {
		return bad()
	}
	var task osreinstall.Task
	if json.Unmarshal([]byte(raw), &task) != nil {
		return bad()
	}
	task.RecoveryPassword = string(password)
	if task.Validate() != nil {
		return bad()
	}
	access, err := osreinstall.OpenAccessSnapshot(evidence.AccessCiphertext, string(password), taskID)
	if err != nil || osreinstall.ValidateAccessSnapshot(access, task.Username, task.SSHPort, task.Inspection.SSHHostKeySHA256) != nil {
		return bad()
	}
	plan, observed := evidence.Network, task.Inspection.Network
	if plan.ValidateObservation(observed) != nil {
		return bad()
	}

	// recovery.Inspect reads an encrypted file and checks its archive, database
	// binding and exact Agent release. The temporary copy contains ciphertext
	// only and is removed before returning.
	file, err := os.CreateTemp(s.dataDir, ".os-reinstall-verify-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err == nil {
		var written int
		written, err = file.Write(evidence.Ciphertext)
		if err == nil && written != len(evidence.Ciphertext) {
			err = errors.New("center: truncated reinstall backup")
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	artifact, err := recovery.Inspect(ctx, name, string(password), Version)
	if err != nil || artifact.Digest != evidence.Digest || artifact.Manifest.Kind != "agent" ||
		artifact.Manifest.ComponentID != agentID || artifact.Manifest.IdentityHash != task.IdentitySHA256 {
		return bad()
	}
	started, err := time.Parse(time.RFC3339Nano, created)
	if err != nil || artifact.Manifest.CreatedAt.Before(started.Add(-time.Minute)) || artifact.Manifest.CreatedAt.After(s.now().Add(time.Minute)) {
		return bad()
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	sealedPlan, err := secret.Seal(s.key, planJSON, []byte("os-reinstall-network:"+taskID))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var synchronous int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous < 2 {
		return errors.New("center: durable backup storage is unavailable")
	}
	now = s.now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE task_executions SET phase='backup_ready',updated_at=?
		WHERE id=? AND agent_id=? AND task_id=? AND session_id=? AND kind=? AND state='running'
		AND phase='apply' AND disposition='' AND expires_at>?
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		now, executionID, agentID, taskID, sessionID, osreinstall.TaskKind, now, agentID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	updated, err = tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state='backup_ready',encrypted_backup=?,encrypted_access=?,backup_digest=?,sealed_network=?,updated_at=?
		WHERE id=? AND agent_id=? AND state='running' AND attempt=1 AND lease_expires_at>?`,
		evidence.Ciphertext, evidence.AccessCiphertext, evidence.Digest, sealedPlan, now, taskID, agentID, now)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	// The execution phase update is recorded by the existing audit trigger.
	return tx.Commit()
}

// ArmOSReinstall consumes the one-time permission only after the stored
// recovery artifact has been read back and the Agent has prepared the reviewed
// installer. A lost response leaves the operation armed/unknown: no retry.
func (s *Store) ArmOSReinstall(ctx context.Context, agentID, sessionID, executionID string, evidence osreinstall.ArmEvidence) error {
	bad := func() error { return errors.New("center: reinstall arming evidence rejected") }
	isDigest := func(value string) bool {
		raw, ok := strings.CutPrefix(value, "sha256:")
		if !ok || len(raw) != 64 {
			return false
		}
		decoded, err := hex.DecodeString(raw)
		return err == nil && len(decoded) == sha256.Size && raw == strings.ToLower(raw)
	}
	if agentID == "" || sessionID == "" || executionID == "" ||
		!isDigest(evidence.BackupDigest) || !isDigest(evidence.PreparedDigest) {
		return bad()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	var taskID, savedDigest string
	var backup, sealedPlan, sealedPassword, encryptedAccess []byte
	err = tx.QueryRowContext(ctx, `SELECT q.id,q.backup_digest,q.encrypted_backup,q.sealed_network,q.sealed_password,q.encrypted_access
		FROM os_reinstall_tasks q JOIN task_executions e ON e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt
		JOIN agent_execution_sessions session ON session.agent_id=e.agent_id AND session.session_id=e.session_id
		JOIN agents a ON a.id=q.agent_id
		WHERE e.id=? AND e.agent_id=? AND e.session_id=? AND e.kind=? AND e.state='running'
		AND e.phase='backup_ready' AND e.disposition='' AND e.expires_at>?
		AND q.state='backup_ready' AND q.attempt=1 AND q.lease_expires_at>?
		AND a.status='active' AND a.credential_revoked_at=''`,
		executionID, agentID, sessionID, osreinstall.TaskKind, stamp, stamp).
		Scan(&taskID, &savedDigest, &backup, &sealedPlan, &sealedPassword, &encryptedAccess)
	if err != nil || savedDigest != evidence.BackupDigest || recovery.Digest(backup) != savedDigest {
		return bad()
	}
	plain, err := secret.Open(s.key, sealedPlan, []byte("os-reinstall-network:"+taskID))
	if err != nil {
		return bad()
	}
	var plan osreinstall.StaticNetworkPlan
	if json.Unmarshal(plain, &plan) != nil {
		return bad()
	}
	if _, _, _, err = plan.RenderStaticNetwork(); err != nil {
		return bad()
	}
	password, err := secret.Open(s.key, sealedPassword, []byte("os-reinstall-password:"+taskID))
	if err != nil {
		return bad()
	}
	if _, err = osreinstall.OpenAccessSnapshot(encryptedAccess, string(password), taskID); err != nil {
		return bad()
	}
	changed, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='helper_running',phase='reinstall_armed',expires_at=?,updated_at=?
		WHERE id=? AND agent_id=? AND task_id=? AND session_id=? AND kind=? AND state='running'
		AND phase='backup_ready' AND disposition='' AND expires_at>?
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		now.Add(45*time.Minute).Format(time.RFC3339Nano), stamp, executionID, agentID, taskID, sessionID, osreinstall.TaskKind, stamp, agentID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	changed, err = tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state='armed',prepared_digest=?,lease_expires_at='',updated_at=?
		WHERE id=? AND agent_id=? AND state='backup_ready' AND attempt=1 AND backup_digest=? AND lease_expires_at>?`,
		evidence.PreparedDigest, stamp, taskID, agentID, evidence.BackupDigest, stamp)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	return tx.Commit()
}

// verifyOSReinstallKeyBinding protects root-key-bound recovery fields during
// database backup/restore, including pending tasks and retained task history.
func verifyOSReinstallKeyBinding(ctx context.Context, db *sql.DB, key []byte) error {
	exists, err := tableHasColumns(ctx, db, "os_reinstall_tasks", "id", "sealed_password", "sealed_network")
	if err != nil || !exists {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,sealed_password,sealed_network FROM os_reinstall_tasks`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var password, network []byte
		if err := rows.Scan(&id, &password, &network); err != nil {
			return err
		}
		if _, err := secret.Open(key, password, []byte("os-reinstall-password:"+id)); err != nil {
			return fmt.Errorf("center: OS reinstall password does not match the root key: %w", err)
		}
		if len(network) > 0 {
			if _, err := secret.Open(key, network, []byte("os-reinstall-network:"+id)); err != nil {
				return fmt.Errorf("center: OS reinstall network does not match the root key: %w", err)
			}
		}
	}
	return rows.Err()
}
