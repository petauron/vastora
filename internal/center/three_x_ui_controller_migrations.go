package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

const (
	threeXUIBackupInterval = time.Hour
	threeXUIBackupMaxSize  = 64 << 20
)

type ThreeXUIBackupView struct {
	ApplicationID string    `json:"applicationId"`
	Revision      int64     `json:"revision"`
	State         string    `json:"state"`
	SHA256        string    `json:"sha256,omitempty"`
	Size          int64     `json:"size"`
	LastError     string    `json:"lastError,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ThreeXUIControllerMigrationView struct {
	ID                  string              `json:"id"`
	SiteID              string              `json:"siteId"`
	SourceApplicationID string              `json:"sourceApplicationId"`
	TargetApplicationID string              `json:"targetApplicationId"`
	BackupRevision      int64               `json:"backupRevision"`
	State               string              `json:"state"`
	Step                string              `json:"step"`
	LastError           string              `json:"lastError,omitempty"`
	Backup              *ThreeXUIBackupView `json:"backup,omitempty"`
	CreatedAt           time.Time           `json:"createdAt"`
	UpdatedAt           time.Time           `json:"updatedAt"`
}

type ThreeXUIControllerMigrationInput struct {
	TargetApplicationID string `json:"targetApplicationId"`
	Confirm             bool   `json:"confirm"`
	AllowStaleBackup    bool   `json:"allowStaleBackup"`
}

func threeXUIBackupAAD(applicationID string, revision int64) []byte {
	return []byte(fmt.Sprintf("three-x-ui-backup:%s:%d", applicationID, revision))
}

func (s *Store) queueScheduledThreeXUIBackup(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) error {
	var applicationID string
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM applications a
		LEFT JOIN three_x_ui_backups b ON b.application_id = a.id
		WHERE a.node_id = ? AND a.app_key = ? AND a.role = 'master' AND a.status = 'running'
		AND (b.application_id IS NULL OR b.state = 'failed' OR b.updated_at < ?)
		AND NOT EXISTS (SELECT 1 FROM application_commands c WHERE c.application_id = a.id AND c.state IN ('pending', 'running'))
		AND NOT EXISTS (SELECT 1 FROM three_x_ui_migrations m WHERE m.site_id = a.site_id AND m.state IN ('backing_up', 'restoring', 'switching'))
		LIMIT 1`, agentID, threeXUIAppKey, now.Add(-threeXUIBackupInterval).Format(time.RFC3339Nano)).Scan(&applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.queueThreeXUIControllerBackup(ctx, tx, applicationID, agentID, "", now)
	return err
}

func (s *Store) queueThreeXUIControllerBackup(ctx context.Context, tx *sql.Tx, applicationID, agentID, migrationID string, now time.Time) (int64, error) {
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM three_x_ui_backups WHERE application_id = ?`, applicationID).Scan(&revision); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_backups(application_id, revision, state, created_at, updated_at)
		VALUES(?, ?, 'pending', ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET revision = excluded.revision, state = 'pending', sealed = NULL, sha256 = '', size = 0, last_error = '', updated_at = excluded.updated_at`,
		applicationID, revision, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return 0, err
	}
	command := ThreeXUIControllerCommandTask{Action: "backup", MigrationID: migrationID, ApplicationID: applicationID, BackupRevision: revision}
	if err := s.queueThreeXUIControllerCommand(ctx, tx, applicationID, agentID, command, now, "3x-ui controller restore point queued"); err != nil {
		return 0, err
	}
	return revision, nil
}

func (s *Store) queueThreeXUIControllerCommand(ctx context.Context, tx *sql.Tx, applicationID, agentID string, command ThreeXUIControllerCommandTask, now time.Time, message string) error {
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	token, err := randomToken(18)
	if err != nil {
		return err
	}
	id := "application-command-" + token
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, applicationID, agentID, agentID, controllerCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: queue 3x-ui controller operation: %w", err)
	}
	return s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", message)
}

func (s *Store) CreateThreeXUIControllerMigration(ctx context.Context, sourceApplicationID string, input ThreeXUIControllerMigrationInput) (ThreeXUIControllerMigrationView, error) {
	sourceApplicationID = strings.TrimSpace(sourceApplicationID)
	input.TargetApplicationID = strings.TrimSpace(input.TargetApplicationID)
	if sourceApplicationID == "" || input.TargetApplicationID == "" || sourceApplicationID == input.TargetApplicationID || !input.Confirm {
		return ThreeXUIControllerMigrationView{}, errors.New("center: source, target, and migration confirmation are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	defer tx.Rollback()
	type endpoint struct {
		applicationID, siteID, agentID, role, status, name, address, lastSeen string
		panelPort, remoteNodeID                                               int
	}
	var source, target endpoint
	if err := tx.QueryRowContext(ctx, `SELECT a.id, a.site_id, a.node_id, a.role, a.status, n.name, COALESCE(p.service_address, ''), n.last_seen_at,
		COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), 0
		FROM applications a JOIN agents n ON n.id = a.node_id
		LEFT JOIN agent_network_profiles p ON p.agent_id = n.id
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = a.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE a.id = ? AND a.app_key = ?`, sourceApplicationID, threeXUIAppKey).Scan(&source.applicationID, &source.siteID, &source.agentID, &source.role, &source.status, &source.name, &source.address, &source.lastSeen, &source.panelPort, &source.remoteNodeID); errors.Is(err, sql.ErrNoRows) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: running 3x-ui subscription controller was not found")
	} else if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if source.role != threeXUIRoleMaster || source.status != "running" || net.ParseIP(source.address) == nil {
		return ThreeXUIControllerMigrationView{}, errors.New("center: source is not a ready 3x-ui subscription controller")
	}
	if err := tx.QueryRowContext(ctx, `SELECT a.id, a.site_id, a.node_id, a.role, a.status, n.name, COALESCE(p.service_address, ''), n.last_seen_at,
		COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), COALESCE(t.remote_node_id, 0)
		FROM applications a JOIN agents n ON n.id = a.node_id
		LEFT JOIN agent_network_profiles p ON p.agent_id = n.id
		JOIN three_x_ui_nodes t ON t.worker_application_id = a.id AND t.master_application_id = ? AND t.status = 'ready'
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = a.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE a.id = ? AND a.app_key = ?`, sourceApplicationID, input.TargetApplicationID, threeXUIAppKey).Scan(&target.applicationID, &target.siteID, &target.agentID, &target.role, &target.status, &target.name, &target.address, &target.lastSeen, &target.panelPort, &target.remoteNodeID); errors.Is(err, sql.ErrNoRows) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: choose a connected VLESS node as the new subscription host")
	} else if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if target.siteID != source.siteID || target.role != threeXUIRoleWorker || target.status != "running" || target.remoteNodeID < 1 || net.ParseIP(target.address) == nil {
		return ThreeXUIControllerMigrationView{}, errors.New("center: target VLESS node is not ready for controller migration")
	}
	targetSeen, _ := time.Parse(time.RFC3339Nano, target.lastSeen)
	if !targetSeen.After(s.now().Add(-45 * time.Second)) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: target node is offline")
	}
	sourceSeen, _ := time.Parse(time.RFC3339Nano, source.lastSeen)
	sourceOnline := sourceSeen.After(s.now().Add(-45 * time.Second))
	if !sourceOnline && !input.AllowStaleBackup {
		return ThreeXUIControllerMigrationView{}, errors.New("center: source node is offline; confirm using the latest restore point")
	}
	if !sourceOnline && input.AllowStaleBackup {
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = 'superseded by offline controller recovery', updated_at = ? WHERE application_id = ? AND state IN ('pending', 'running')`, s.now().UTC().Format(time.RFC3339Nano), sourceApplicationID); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
	}
	var activeCommands int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id IN (?, ?) AND state IN ('pending', 'running')`, sourceApplicationID, input.TargetApplicationID).Scan(&activeCommands); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if activeCommands != 0 {
		return ThreeXUIControllerMigrationView{}, errors.New("center: wait for the current 3x-ui operation to finish before migrating")
	}
	idToken, err := randomToken(18)
	if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	id := "three-x-ui-migration-" + idToken
	now := s.now().UTC()
	state, step := "backing_up", "backup"
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_migrations(id, site_id, source_application_id, target_application_id, state, step, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, id, source.siteID, sourceApplicationID, input.TargetApplicationID, state, step, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return ThreeXUIControllerMigrationView{}, fmt.Errorf("center: start 3x-ui controller migration: %w", err)
	}
	if sourceOnline {
		revision, err := s.queueThreeXUIControllerBackup(ctx, tx, sourceApplicationID, source.agentID, id, now)
		if err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET backup_revision = ? WHERE id = ?`, revision, id); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
	} else {
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM three_x_ui_backups WHERE application_id = ? AND state = 'ready' AND sealed IS NOT NULL`, sourceApplicationID).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
			return ThreeXUIControllerMigrationView{}, errors.New("center: source is offline and no restore point is available")
		} else if err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if err := s.queueThreeXUIPromote(ctx, tx, id, source, target, revision, now); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	return s.ThreeXUIControllerMigration(ctx, id)
}

func (s *Store) queueThreeXUIPromote(ctx context.Context, tx *sql.Tx, migrationID string, source, target struct {
	applicationID, siteID, agentID, role, status, name, address, lastSeen string
	panelPort, remoteNodeID                                               int
}, revision int64, now time.Time) error {
	command := ThreeXUIControllerCommandTask{Action: "promote", MigrationID: migrationID, ApplicationID: target.applicationID, SourceApplicationID: source.applicationID, SourceName: source.name, SourceAddress: source.address, SourcePanelPort: source.panelPort, SourceRemoteNodeID: target.remoteNodeID, BackupRevision: revision}
	if err := s.queueThreeXUIControllerCommand(ctx, tx, target.applicationID, target.agentID, command, now, "3x-ui restore point queued on the new subscription host"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET backup_revision = ?, state = 'restoring', step = 'restore', last_error = '', updated_at = ? WHERE id = ?`, revision, now.Format(time.RFC3339Nano), migrationID)
	return err
}

func (s *Store) ListThreeXUIControllerMigrations(ctx context.Context) ([]ThreeXUIControllerMigrationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.site_id, m.source_application_id, m.target_application_id, m.backup_revision, m.state, m.step, m.last_error, m.created_at, m.updated_at,
		COALESCE(b.application_id, ''), COALESCE(b.revision, 0), COALESCE(b.state, ''), COALESCE(b.sha256, ''), COALESCE(b.size, 0), COALESCE(b.last_error, ''), COALESCE(b.updated_at, '')
		FROM three_x_ui_migrations m LEFT JOIN three_x_ui_backups b ON b.application_id = m.source_application_id AND b.revision = m.backup_revision
		ORDER BY m.created_at DESC, m.rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ThreeXUIControllerMigrationView{}
	for rows.Next() {
		var value ThreeXUIControllerMigrationView
		var created, updated, backupApplication, backupState, backupSHA, backupError, backupUpdated string
		var backupRevision, backupSize int64
		if err := rows.Scan(&value.ID, &value.SiteID, &value.SourceApplicationID, &value.TargetApplicationID, &value.BackupRevision, &value.State, &value.Step, &value.LastError, &created, &updated, &backupApplication, &backupRevision, &backupState, &backupSHA, &backupSize, &backupError, &backupUpdated); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if backupApplication != "" {
			backup := &ThreeXUIBackupView{ApplicationID: backupApplication, Revision: backupRevision, State: backupState, SHA256: backupSHA, Size: backupSize, LastError: backupError}
			backup.UpdatedAt, _ = time.Parse(time.RFC3339Nano, backupUpdated)
			value.Backup = backup
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ThreeXUIControllerMigration(ctx context.Context, id string) (ThreeXUIControllerMigrationView, error) {
	values, err := s.ListThreeXUIControllerMigrations(ctx)
	if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, nil
		}
	}
	return ThreeXUIControllerMigrationView{}, errors.New("center: 3x-ui controller migration not found")
}

func (s *Store) StoreThreeXUIBackup(ctx context.Context, agentID, credential, applicationID string, revision int64, reader io.Reader) (ThreeXUIBackupView, error) {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return ThreeXUIBackupView{}, err
	}
	if revision < 1 {
		return ThreeXUIBackupView{}, errors.New("center: invalid restore point revision")
	}
	content, err := io.ReadAll(io.LimitReader(reader, threeXUIBackupMaxSize+1))
	if err != nil {
		return ThreeXUIBackupView{}, err
	}
	if len(content) < 16 || len(content) > threeXUIBackupMaxSize || string(content[:16]) != "SQLite format 3\x00" {
		return ThreeXUIBackupView{}, errors.New("center: Agent returned an invalid 3x-ui SQLite restore point")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ThreeXUIBackupView{}, err
	}
	defer tx.Rollback()
	var inputJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT input_json FROM application_commands WHERE application_id = ? AND agent_id = ? AND kind = ? AND state = 'running' ORDER BY updated_at DESC LIMIT 1`, applicationID, agentID, controllerCommandKind).Scan(&inputJSON); err != nil {
		return ThreeXUIBackupView{}, errors.New("center: no active restore point operation")
	}
	var command ThreeXUIControllerCommandTask
	if json.Unmarshal(inputJSON, &command) != nil || command.Action != "backup" || command.BackupRevision != revision {
		return ThreeXUIBackupView{}, errors.New("center: restore point does not match the active operation")
	}
	digest := sha256.Sum256(content)
	digestString := hex.EncodeToString(digest[:])
	var existingState, existingSHA, existingUpdated string
	var existingSize int64
	if err := tx.QueryRowContext(ctx, `SELECT state, sha256, size, updated_at FROM three_x_ui_backups WHERE application_id = ? AND revision = ?`, applicationID, revision).Scan(&existingState, &existingSHA, &existingSize, &existingUpdated); err != nil {
		return ThreeXUIBackupView{}, err
	}
	if existingState == "ready" {
		if existingSHA != digestString || existingSize != int64(len(content)) {
			return ThreeXUIBackupView{}, errors.New("center: retry restore point content does not match the stored revision")
		}
		updated, _ := time.Parse(time.RFC3339Nano, existingUpdated)
		return ThreeXUIBackupView{ApplicationID: applicationID, Revision: revision, State: "ready", SHA256: existingSHA, Size: existingSize, UpdatedAt: updated}, tx.Commit()
	}
	if existingState != "pending" {
		return ThreeXUIBackupView{}, errors.New("center: restore point operation is stale")
	}
	sealed, err := secret.Seal(s.key, content, threeXUIBackupAAD(applicationID, revision))
	if err != nil {
		return ThreeXUIBackupView{}, err
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE three_x_ui_backups SET state = 'ready', sealed = ?, sha256 = ?, size = ?, last_error = '', updated_at = ? WHERE application_id = ? AND revision = ? AND state = 'pending'`, sealed, digestString, len(content), now.Format(time.RFC3339Nano), applicationID, revision)
	if err != nil {
		return ThreeXUIBackupView{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ThreeXUIBackupView{}, errors.New("center: restore point operation is stale")
	}
	if err := tx.Commit(); err != nil {
		return ThreeXUIBackupView{}, err
	}
	return ThreeXUIBackupView{ApplicationID: applicationID, Revision: revision, State: "ready", SHA256: digestString, Size: int64(len(content)), UpdatedAt: now}, nil
}

func (s *Store) ThreeXUIMigrationBackup(ctx context.Context, agentID, credential, migrationID string) ([]byte, error) {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return nil, err
	}
	var sourceApplicationID, targetApplicationID string
	var revision int64
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT m.source_application_id, m.target_application_id, m.backup_revision, b.sealed
		FROM three_x_ui_migrations m JOIN applications target ON target.id = m.target_application_id
		JOIN three_x_ui_backups b ON b.application_id = m.source_application_id AND b.revision = m.backup_revision
		JOIN application_commands c ON c.application_id = m.target_application_id AND c.agent_id = target.node_id AND c.kind = ? AND c.state = 'running'
			AND json_extract(c.input_json, '$.action') = 'promote' AND json_extract(c.input_json, '$.migrationId') = m.id
		WHERE m.id = ? AND target.node_id = ? AND m.state = 'restoring' AND b.state = 'ready'`, controllerCommandKind, migrationID, agentID).Scan(&sourceApplicationID, &targetApplicationID, &revision, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: active 3x-ui migration restore point not found")
	}
	if err != nil {
		return nil, err
	}
	_ = targetApplicationID
	return secret.Open(s.key, sealed, threeXUIBackupAAD(sourceApplicationID, revision))
}

func (s *Store) completeThreeXUIControllerCommand(ctx context.Context, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input ThreeXUIControllerCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.ApplicationID == "" {
		return errors.New("center: stored 3x-ui controller operation is invalid")
	}
	var envelope ApplicationTaskResult
	if succeeded && (len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ControllerCommand == nil || envelope.ControllerCommand.Action != input.Action) {
		succeeded = false
		taskError = "center: Agent returned an invalid 3x-ui controller result"
	}
	now := s.now().UTC()
	if taskError == "" && !succeeded {
		taskError = "3x-ui controller operation failed"
	}
	state, event, message := "succeeded", "succeeded", "3x-ui controller operation completed"
	if !succeeded {
		state, event, message = "failed", "failed", taskError
	}
	resultJSON := []byte(`{}`)
	if succeeded {
		resultJSON, _ = json.Marshal(envelope.ControllerCommand)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now.Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	if !succeeded {
		if input.Action == "backup" {
			_, _ = tx.ExecContext(ctx, `UPDATE three_x_ui_backups SET state = 'failed', last_error = ?, updated_at = ? WHERE application_id = ? AND revision = ?`, taskError, now.Format(time.RFC3339Nano), input.ApplicationID, input.BackupRevision)
		}
		if input.MigrationID != "" {
			if input.Action == "demote" {
				_, _ = tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'switching', step = 'cleanup', last_error = ?, updated_at = ? WHERE id = ?`, taskError, now.Format(time.RFC3339Nano), input.MigrationID)
				_, _ = tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'failed', last_error = ?, updated_at = ? WHERE worker_application_id = ?`, taskError, now.Format(time.RFC3339Nano), input.ApplicationID)
			} else {
				_, _ = tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'failed', last_error = ?, updated_at = ? WHERE id = ?`, taskError, now.Format(time.RFC3339Nano), input.MigrationID)
			}
		}
	} else {
		switch input.Action {
		case "backup":
			var backupState, backupSHA string
			var backupSize int64
			if err := tx.QueryRowContext(ctx, `SELECT state, sha256, size FROM three_x_ui_backups WHERE application_id = ? AND revision = ?`, input.ApplicationID, input.BackupRevision).Scan(&backupState, &backupSHA, &backupSize); err != nil || backupState != "ready" || envelope.ControllerCommand.BackupRevision != input.BackupRevision || envelope.ControllerCommand.BackupSHA256 != backupSHA || envelope.ControllerCommand.BackupSize != backupSize {
				return errors.New("center: Agent completed backup before uploading its restore point")
			}
			if input.MigrationID != "" {
				var source, target struct {
					applicationID, siteID, agentID, role, status, name, address, lastSeen string
					panelPort, remoteNodeID                                               int
				}
				if err := s.loadThreeXUIMigrationEndpoints(ctx, tx, input.MigrationID, &source, &target); err != nil {
					return err
				}
				if err := s.queueThreeXUIPromote(ctx, tx, input.MigrationID, source, target, input.BackupRevision, now); err != nil {
					return err
				}
			}
		case "promote":
			if envelope.ControllerCommand.SourceRemoteNodeID != input.SourceRemoteNodeID || envelope.ControllerCommand.SourceRemoteNodeID < 1 {
				return errors.New("center: Agent returned an invalid promoted controller topology")
			}
			if err := s.switchThreeXUIController(ctx, tx, input, now); err != nil {
				return err
			}
			message = "3x-ui subscription host migrated"
		case "demote":
			if err := s.queueThreeXUINodeReconcileAfterDemotion(ctx, tx, input.ApplicationID, input.MigrationID, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET step = 'switch', last_error = '', updated_at = ? WHERE id = ? AND state = 'switching'`, now.Format(time.RFC3339Nano), input.MigrationID); err != nil {
				return err
			}
			message = "previous 3x-ui subscription host prepared to reconnect as a VLESS node"
		}
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryThreeXUIControllerMigrationCleanup retries only the old controller
// demotion. The new controller has already taken ownership at this stage, so
// restarting the whole migration would restore stale data over the live copy.
func (s *Store) RetryThreeXUIControllerMigrationCleanup(ctx context.Context, migrationID string) (ThreeXUIControllerMigrationView, error) {
	migrationID = strings.TrimSpace(migrationID)
	if migrationID == "" {
		return ThreeXUIControllerMigrationView{}, errors.New("center: 3x-ui controller migration is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	defer tx.Rollback()
	var sourceApplicationID, sourceAgentID, state, lastError string
	if err := tx.QueryRowContext(ctx, `SELECT m.source_application_id, source.node_id, m.state, m.last_error
		FROM three_x_ui_migrations m JOIN applications source ON source.id = m.source_application_id
		WHERE m.id = ?`, migrationID).Scan(&sourceApplicationID, &sourceAgentID, &state, &lastError); errors.Is(err, sql.ErrNoRows) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: 3x-ui controller migration not found")
	} else if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if state != "switching" || lastError == "" {
		return ThreeXUIControllerMigrationView{}, errors.New("center: old 3x-ui controller cleanup does not need a retry")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id = ? AND kind = ? AND state IN ('pending', 'running')`, sourceApplicationID, controllerCommandKind).Scan(&active); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if active != 0 {
		return ThreeXUIControllerMigrationView{}, errors.New("center: old 3x-ui controller cleanup is already running")
	}
	now := s.now().UTC()
	command := ThreeXUIControllerCommandTask{Action: "demote", MigrationID: migrationID, ApplicationID: sourceApplicationID}
	if err := s.queueThreeXUIControllerCommand(ctx, tx, sourceApplicationID, sourceAgentID, command, now, "previous 3x-ui subscription host cleanup retried"); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET step = 'cleanup', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), migrationID); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'pending', last_error = '', updated_at = ? WHERE worker_application_id = ?`, now.Format(time.RFC3339Nano), sourceApplicationID); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	return s.ThreeXUIControllerMigration(ctx, migrationID)
}

func (s *Store) loadThreeXUIMigrationEndpoints(ctx context.Context, tx *sql.Tx, migrationID string, source, target *struct {
	applicationID, siteID, agentID, role, status, name, address, lastSeen string
	panelPort, remoteNodeID                                               int
}) error {
	var sourceID, targetID string
	if err := tx.QueryRowContext(ctx, `SELECT source_application_id, target_application_id FROM three_x_ui_migrations WHERE id = ?`, migrationID).Scan(&sourceID, &targetID); err != nil {
		return err
	}
	query := `SELECT a.id, a.site_id, a.node_id, a.role, a.status, n.name, COALESCE(p.service_address, ''), n.last_seen_at, COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), COALESCE(t.remote_node_id, 0)
		FROM applications a JOIN agents n ON n.id = a.node_id LEFT JOIN agent_network_profiles p ON p.agent_id = n.id
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = a.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		LEFT JOIN three_x_ui_nodes t ON t.worker_application_id = a.id WHERE a.id = ?`
	if err := tx.QueryRowContext(ctx, query, sourceID).Scan(&source.applicationID, &source.siteID, &source.agentID, &source.role, &source.status, &source.name, &source.address, &source.lastSeen, &source.panelPort, &source.remoteNodeID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, query, targetID).Scan(&target.applicationID, &target.siteID, &target.agentID, &target.role, &target.status, &target.name, &target.address, &target.lastSeen, &target.panelPort, &target.remoteNodeID); err != nil {
		return err
	}
	return nil
}

func (s *Store) switchThreeXUIController(ctx context.Context, tx *sql.Tx, input ThreeXUIControllerCommandTask, now time.Time) error {
	var siteID, sourceAgentID, targetAgentID, targetAddress string
	var targetPanelPort, targetRemoteNodeID int
	if err := tx.QueryRowContext(ctx, `SELECT source.site_id, source.node_id, target.node_id, COALESCE(p.service_address, ''), COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), topology.remote_node_id
		FROM applications source JOIN applications target ON target.id = ? JOIN agent_network_profiles p ON p.agent_id = target.node_id
		JOIN three_x_ui_nodes topology ON topology.worker_application_id = target.id AND topology.master_application_id = source.id
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = target.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE source.id = ? AND source.role = 'master' AND target.role = 'worker'`, input.ApplicationID, input.SourceApplicationID).Scan(&siteID, &sourceAgentID, &targetAgentID, &targetAddress, &targetPanelPort, &targetRemoteNodeID); err != nil {
		return errors.New("center: 3x-ui controller topology changed during migration")
	}
	if targetRemoteNodeID != input.SourceRemoteNodeID || net.ParseIP(targetAddress) == nil {
		return errors.New("center: 3x-ui migration target changed during restore")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET role = 'worker', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), input.SourceApplicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET role = 'master', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), input.ApplicationID); err != nil {
		return err
	}
	if err := s.copyApplicationSecrets(ctx, tx, input.SourceApplicationID, input.ApplicationID, now); err != nil {
		return err
	}
	if err := s.moveThreeXUICatalogServices(ctx, tx, input.SourceApplicationID, input.ApplicationID, siteID, targetAddress, targetPanelPort, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_nodes WHERE worker_application_id = ?`, input.ApplicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET master_application_id = ?, status = 'pending', last_error = '', updated_at = ? WHERE master_application_id = ?`, input.ApplicationID, now.Format(time.RFC3339Nano), input.SourceApplicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id, master_application_id, remote_node_id, status, last_error, created_at, updated_at)
		VALUES(?, ?, ?, 'pending', '', ?, ?)`, input.SourceApplicationID, input.ApplicationID, input.SourceRemoteNodeID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'switching', step = 'cleanup', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), input.MigrationID); err != nil {
		return err
	}
	// Commands still pointed at the old controller must never be replayed after the role switch.
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = 'superseded by controller migration', updated_at = ?
		WHERE kind = ? AND agent_id = ? AND state IN ('pending','running')`, now.Format(time.RFC3339Nano), nodeCommandKind, sourceAgentID); err != nil {
		return err
	}
	command := ThreeXUIControllerCommandTask{Action: "demote", MigrationID: input.MigrationID, ApplicationID: input.SourceApplicationID}
	if err := s.queueThreeXUIControllerCommand(ctx, tx, input.SourceApplicationID, sourceAgentID, command, now, "previous 3x-ui subscription host queued for VLESS-only mode"); err != nil {
		return err
	}
	return s.queueOtherThreeXUINodesAfterMigration(ctx, tx, input.MigrationID, input.ApplicationID, input.SourceApplicationID, now)
}

func (s *Store) copyApplicationSecrets(ctx context.Context, tx *sql.Tx, sourceApplicationID, targetApplicationID string, now time.Time) error {
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT s.sealed FROM application_secrets a JOIN secrets s ON s.id = a.secret_id WHERE a.application_id = ?`, sourceApplicationID).Scan(&sealed); err != nil {
		return err
	}
	plain, err := secret.Open(s.key, sealed, []byte("application:"+sourceApplicationID))
	if err != nil {
		return err
	}
	secretID, err := s.putSecret(ctx, tx, plain, "application:"+targetApplicationID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_secrets(application_id, secret_id, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET secret_id = excluded.secret_id, updated_at = excluded.updated_at`, targetApplicationID, secretID, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var deploymentID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE application_id = ? AND state = 'succeeded' AND operation IN ('install','upgrade','configure') ORDER BY updated_at DESC, rowid DESC LIMIT 1`, targetApplicationID).Scan(&deploymentID); err != nil {
		return err
	}
	deploymentSecretID, err := s.putSecret(ctx, tx, plain, "deployment:"+deploymentID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE deployments SET secret_id = ? WHERE id = ?`, deploymentSecretID, deploymentID)
	return err
}

func (s *Store) moveThreeXUICatalogServices(ctx context.Context, tx *sql.Tx, sourceApplicationID, targetApplicationID, siteID, targetAddress string, panelPort int, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, protocol, container_port, host_port, management FROM services WHERE application_id = ? AND source = 'catalog'`, sourceApplicationID)
	if err != nil {
		return err
	}
	type service struct {
		id, name, protocol                  string
		containerPort, hostPort, management int
	}
	values := []service{}
	for rows.Next() {
		var value service
		if err := rows.Scan(&value.id, &value.name, &value.protocol, &value.containerPort, &value.hostPort, &value.management); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		hostPort := value.hostPort
		if value.name == "panel" {
			hostPort = panelPort
		} else if value.name == "subscription" {
			hostPort = 2096
		}
		endpoint := net.JoinHostPort(targetAddress, fmt.Sprint(hostPort))
		var targetServiceID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE application_id = ? AND name = ?`, targetApplicationID, value.name).Scan(&targetServiceID)
		if errors.Is(err, sql.ErrNoRows) {
			targetServiceID, err = randomToken(18)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, status, created_at, updated_at)
					VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'catalog', '', ?, 'ready', ?, ?)`, targetServiceID, targetApplicationID, siteID, value.name, value.protocol, value.containerPort, hostPort, endpoint, value.management, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			}
		} else if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE services SET protocol = ?, container_port = ?, host_port = ?, endpoint = ?, status = 'ready', last_error = '', updated_at = ? WHERE id = ?`, value.protocol, value.containerPort, hostPort, endpoint, now.Format(time.RFC3339Nano), targetServiceID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET service_id = ?, status = 'pending', desired_revision = desired_revision + 1, last_error = '', updated_at = ? WHERE service_id = ?`, targetServiceID, now.Format(time.RFC3339Nano), value.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET service_id = ?, upstreams_json = ?, status = 'pending', last_error = '', updated_at = ? WHERE service_id = ?`, targetServiceID, mustJSON([]string{endpoint}), now.Format(time.RFC3339Nano), value.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), value.id); err != nil {
			return err
		}
	}
	return s.reconcileApplicationPublications(ctx, tx, targetApplicationID, now)
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (s *Store) queueOtherThreeXUINodesAfterMigration(ctx context.Context, tx *sql.Tx, migrationID, masterApplicationID, sourceApplicationID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT n.worker_application_id, d.id FROM three_x_ui_nodes n
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = n.worker_application_id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE n.master_application_id = ? AND n.worker_application_id <> ?`, masterApplicationID, sourceApplicationID)
	if err != nil {
		return err
	}
	type item struct{ applicationID, deploymentID string }
	items := []item{}
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.applicationID, &value.deploymentID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range items {
		if err := s.queueThreeXUINodeReconcile(ctx, tx, value.deploymentID, value.applicationID, migrationID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueThreeXUINodeReconcileAfterDemotion(ctx context.Context, tx *sql.Tx, applicationID, migrationID string, now time.Time) error {
	var deploymentID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE application_id = ? AND state = 'succeeded' AND operation IN ('install','upgrade','configure') ORDER BY updated_at DESC, rowid DESC LIMIT 1`, applicationID).Scan(&deploymentID); err != nil {
		return err
	}
	return s.queueThreeXUINodeReconcile(ctx, tx, deploymentID, applicationID, migrationID, now)
}
