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
	Kind                string              `json:"kind"`
	SiteID              string              `json:"siteId"`
	SourceApplicationID string              `json:"sourceApplicationId"`
	TargetApplicationID string              `json:"targetApplicationId"`
	BackupRevision      int64               `json:"backupRevision"`
	State               string              `json:"state"`
	Step                string              `json:"step"`
	LastError           string              `json:"lastError,omitempty"`
	FailedWorkerID      string              `json:"failedWorkerApplicationId,omitempty"`
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
		AND NOT EXISTS (SELECT 1 FROM application_commands c WHERE c.application_id = a.id AND (c.state IN ('pending', 'running') OR c.reconciliation_required = 1))
		AND NOT EXISTS (SELECT 1 FROM three_x_ui_migrations m WHERE m.state IN ('backing_up', 'restoring', 'switching'))
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

// resumeThreeXUIControllerConvergence starts at most one durable Alpha
// convergence workflow. Completion invokes this function again, so legacy Site
// controllers are converted sequentially and never in parallel.
func (s *Store) resumeThreeXUIControllerConvergence(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var activeMigrations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching')`).Scan(&activeMigrations); err != nil {
		return err
	}
	if activeMigrations != 0 {
		return tx.Commit()
	}

	controllerApplicationID, _, err := runningGlobalThreeXUIController(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	// A failed conversion is an explicit recovery boundary. Do not skip that
	// controller and continue changing other hosts behind the operator's back.
	var unresolvedFailures int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM three_x_ui_migrations migration
		JOIN applications source ON source.id = migration.source_application_id
		WHERE migration.kind = 'consolidate' AND migration.state = 'failed'
		AND source.role = 'master' AND source.status IN ('pending', 'deploying', 'running')`).Scan(&unresolvedFailures); err != nil {
		return err
	}
	if unresolvedFailures != 0 {
		return tx.Commit()
	}

	// Starting a migration while a data-plane operation is active would make
	// that operation's completion fail the global exclusion triggers. The task
	// completion hook invokes this function again after the operation finishes.
	var activeOperations int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM application_commands command
		 JOIN applications application ON application.id = command.application_id
		 WHERE application.app_key = ? AND (command.state IN ('pending', 'running') OR command.reconciliation_required = 1))
		+
		(SELECT COUNT(*) FROM deployments deployment
		 WHERE deployment.app_key = ? AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1))`, threeXUIAppKey, threeXUIAppKey).Scan(&activeOperations); err != nil {
		return err
	}
	if activeOperations != 0 {
		return tx.Commit()
	}

	var sourceApplicationID, sourceSiteID, sourceAgentID string
	err = tx.QueryRowContext(ctx, `SELECT application.id, application.site_id, application.node_id
		FROM applications application
		WHERE application.app_key = ? AND application.role = 'master' AND application.status = 'running'
		AND application.id <> ?
		ORDER BY application.created_at, application.id
		LIMIT 1`, threeXUIAppKey, controllerApplicationID).Scan(&sourceApplicationID, &sourceSiteID, &sourceAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	token, err := randomToken(18)
	if err != nil {
		return err
	}
	migrationID := "three-x-ui-migration-" + token
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_migrations(
		id, site_id, source_application_id, target_application_id, state, step, created_at, updated_at, kind
	) VALUES(?, ?, ?, ?, 'backing_up', 'backup', ?, ?, 'consolidate')`, migrationID, sourceSiteID, sourceApplicationID, controllerApplicationID, formattedNow, formattedNow); err != nil {
		return fmt.Errorf("center: start automatic 3x-ui controller convergence: %w", err)
	}
	revision, err := s.queueThreeXUIControllerBackup(ctx, tx, sourceApplicationID, sourceAgentID, migrationID, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET backup_revision = ? WHERE id = ?`, revision, migrationID); err != nil {
		return err
	}
	return tx.Commit()
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
	canonicalApplicationID, _, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return ThreeXUIControllerMigrationView{}, errors.New("center: global 3x-ui subscription controller is unavailable")
	}
	kind := "replace"
	if sourceApplicationID != canonicalApplicationID {
		if input.TargetApplicationID != canonicalApplicationID || input.AllowStaleBackup {
			return ThreeXUIControllerMigrationView{}, errors.New("center: legacy controller convergence must target the current global subscription controller")
		}
		kind = "consolidate"
	}
	if kind == "replace" {
		var controllerCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE app_key = ? AND role = 'master' AND status IN ('pending', 'deploying', 'running')`, threeXUIAppKey).Scan(&controllerCount); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if controllerCount != 1 {
			return ThreeXUIControllerMigrationView{}, errors.New("center: consolidate legacy 3x-ui controllers before moving the global subscription host")
		}
	}
	targetQuery := `SELECT a.id, a.site_id, a.node_id, a.role, a.status, n.name, COALESCE(p.service_address, ''), n.last_seen_at,
		COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), COALESCE(t.remote_node_id, 0)
		FROM applications a JOIN agents n ON n.id = a.node_id
		LEFT JOIN agent_network_profiles p ON p.agent_id = n.id
		LEFT JOIN three_x_ui_nodes t ON t.worker_application_id = a.id AND t.master_application_id = ? AND t.status = 'ready'
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = a.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE a.id = ? AND a.app_key = ?`
	if err := tx.QueryRowContext(ctx, targetQuery, sourceApplicationID, input.TargetApplicationID, threeXUIAppKey).Scan(&target.applicationID, &target.siteID, &target.agentID, &target.role, &target.status, &target.name, &target.address, &target.lastSeen, &target.panelPort, &target.remoteNodeID); errors.Is(err, sql.ErrNoRows) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: requested 3x-ui migration target is unavailable")
	} else if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if kind == "replace" && (target.role != threeXUIRoleWorker || target.status != "running" || target.remoteNodeID < 1 || net.ParseIP(target.address) == nil) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: target VLESS node is not ready for controller migration")
	}
	if kind == "consolidate" && (target.applicationID != canonicalApplicationID || target.role != threeXUIRoleMaster || target.status != "running" || net.ParseIP(target.address) == nil) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: global 3x-ui subscription controller is not ready for convergence")
	}
	targetSeen, _ := time.Parse(time.RFC3339Nano, target.lastSeen)
	if !targetSeen.After(s.now().Add(-45 * time.Second)) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: target node is offline")
	}
	sourceSeen, _ := time.Parse(time.RFC3339Nano, source.lastSeen)
	sourceOnline := sourceSeen.After(s.now().Add(-45 * time.Second))
	if kind == "consolidate" && !sourceOnline {
		return ThreeXUIControllerMigrationView{}, errors.New("center: legacy 3x-ui controller must be online before retrying its conversion")
	}
	var unsafeTrafficResets int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_inbound_plans plan
		JOIN services service ON service.id = plan.service_id
		JOIN applications application ON application.id = service.application_id
		WHERE application.app_key = ? AND (plan.status = 'resetting' OR (plan.status = 'failed' AND plan.attempt > 0))`, threeXUIAppKey).Scan(&unsafeTrafficResets); err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if kind == "replace" && sourceOnline && unsafeTrafficResets != 0 {
		return ThreeXUIControllerMigrationView{}, errors.New("center: finish or explicitly replace the pending VLESS traffic reset before migrating the 3x-ui subscription host")
	}
	if kind == "replace" && !sourceOnline && !input.AllowStaleBackup {
		return ThreeXUIControllerMigrationView{}, errors.New("center: source node is offline; confirm using the latest restore point")
	}
	if kind == "replace" && !sourceOnline && input.AllowStaleBackup {
		// An offline controller owns the durable reset journal, so a failover can
		// no longer prove whether the current boundary was applied. Consume that
		// boundary fail-closed and require an explicit plan save after migration;
		// never replay it on the replacement controller.
		var otherSourceDataCommands int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
			WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)
			AND NOT (kind = ? AND json_extract(CASE WHEN json_valid(input_json) THEN input_json ELSE '{}' END, '$.action') = 'reset_inbound_plan')`, source.agentID, controllerCommandKind, clientCommandKind).Scan(&otherSourceDataCommands); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if otherSourceDataCommands != 0 {
			return ThreeXUIControllerMigrationView{}, errors.New("center: the offline subscription host owns an unfinished 3x-ui operation that cannot be recovered safely")
		}
		if unsafeTrafficResets != 0 {
			now := s.now().UTC().Format(time.RFC3339Nano)
			message := "offline controller migration consumed an uncertain traffic reset boundary; inspect the node and explicitly save its plan"
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET status = 'failed', retry_at = '', last_error = ?, updated_at = ?
					WHERE service_id IN (SELECT service.id FROM services service JOIN applications application ON application.id = service.application_id WHERE application.app_key = ?)
					AND (status = 'resetting' OR (status = 'failed' AND attempt > 0))`, message, now, threeXUIAppKey); err != nil {
				return ThreeXUIControllerMigrationView{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = ?, updated_at = ?
				WHERE agent_id = ? AND kind = ? AND state IN ('pending', 'running')
				AND json_extract(CASE WHEN json_valid(input_json) THEN input_json ELSE '{}' END, '$.action') = 'reset_inbound_plan'`, message, now, source.agentID, clientCommandKind); err != nil {
				return ThreeXUIControllerMigrationView{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = 'superseded by offline controller recovery', updated_at = ? WHERE application_id = ? AND state IN ('pending', 'running')`, s.now().UTC().Format(time.RFC3339Nano), sourceApplicationID); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
	}
	var activeCommands int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
		WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND (
			application_id IN (?, ?) OR (agent_id IN (?, ?) AND kind <> ?)
		)`, sourceApplicationID, input.TargetApplicationID, source.agentID, target.agentID, controllerCommandKind).Scan(&activeCommands); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_migrations(id, site_id, source_application_id, target_application_id, state, step, created_at, updated_at, kind) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, source.siteID, sourceApplicationID, input.TargetApplicationID, state, step, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), kind); err != nil {
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
		if kind != "replace" {
			return ThreeXUIControllerMigrationView{}, errors.New("center: legacy 3x-ui controller must be online for convergence")
		}
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

func (s *Store) queueThreeXUIConsolidationDemote(ctx context.Context, tx *sql.Tx, migrationID string, source, target struct {
	applicationID, siteID, agentID, role, status, name, address, lastSeen string
	panelPort, remoteNodeID                                               int
}, now time.Time) error {
	if source.role != threeXUIRoleMaster || source.status != "running" || target.role != threeXUIRoleMaster || target.status != "running" {
		return errors.New("center: legacy 3x-ui controller topology changed during convergence")
	}
	canonicalApplicationID, _, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || target.applicationID != canonicalApplicationID {
		return errors.New("center: global 3x-ui subscription controller changed during convergence")
	}
	command := ThreeXUIControllerCommandTask{Action: "demote", MigrationID: migrationID, ApplicationID: source.applicationID}
	if err := s.queueThreeXUIControllerCommand(ctx, tx, source.applicationID, source.agentID, command, now, "legacy 3x-ui controller backed up and queued for VLESS-only mode"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'switching', step = 'cleanup', last_error = '', updated_at = ? WHERE id = ? AND kind = 'consolidate'`, now.Format(time.RFC3339Nano), migrationID)
	return err
}

func (s *Store) attachConsolidatedThreeXUIController(ctx context.Context, tx *sql.Tx, migrationID string, now time.Time) error {
	var sourceApplicationID, targetApplicationID, sourceRole, targetRole string
	if err := tx.QueryRowContext(ctx, `SELECT migration.source_application_id, migration.target_application_id, source.role, target.role
		FROM three_x_ui_migrations migration
		JOIN applications source ON source.id = migration.source_application_id
		JOIN applications target ON target.id = migration.target_application_id
		WHERE migration.id = ? AND migration.kind = 'consolidate' AND migration.state = 'switching'`, migrationID).Scan(&sourceApplicationID, &targetApplicationID, &sourceRole, &targetRole); err != nil {
		return errors.New("center: legacy 3x-ui controller convergence state changed")
	}
	canonicalApplicationID, _, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || targetApplicationID != canonicalApplicationID || sourceRole != threeXUIRoleMaster || targetRole != threeXUIRoleMaster {
		return errors.New("center: legacy 3x-ui controller topology changed during convergence")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET role = 'worker', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), sourceApplicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes
		SET master_application_id = ?, remote_node_id = NULL,
		status = CASE (SELECT worker.status FROM applications worker WHERE worker.id = three_x_ui_nodes.worker_application_id)
			WHEN 'running' THEN 'pending'
			WHEN 'stopped' THEN 'stopped'
			ELSE 'failed'
		END,
		last_error = CASE (SELECT worker.status FROM applications worker WHERE worker.id = three_x_ui_nodes.worker_application_id)
			WHEN 'running' THEN ''
			WHEN 'stopped' THEN ''
			ELSE 'VLESS node application is not running after legacy controller conversion'
		END,
		updated_at = ?
		WHERE master_application_id = ?`, targetApplicationID, now.Format(time.RFC3339Nano), sourceApplicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id, master_application_id, status, last_error, created_at, updated_at)
		VALUES(?, ?, 'pending', '', ?, ?)
		ON CONFLICT(worker_application_id) DO UPDATE SET master_application_id = excluded.master_application_id,
		remote_node_id = NULL, status = 'pending', last_error = '', updated_at = excluded.updated_at`, sourceApplicationID, targetApplicationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET step = 'switch', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), migrationID); err != nil {
		return err
	}
	var deploymentID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE application_id = ? AND state = 'succeeded' AND operation IN ('install','upgrade','configure') ORDER BY updated_at DESC, rowid DESC LIMIT 1`, sourceApplicationID).Scan(&deploymentID); err != nil {
		return errors.New("center: legacy 3x-ui controller deployment is unavailable for VLESS reconciliation")
	}
	return s.queueThreeXUINodeReconcile(ctx, tx, deploymentID, sourceApplicationID, migrationID, now)
}

func (s *Store) ListThreeXUIControllerMigrations(ctx context.Context) ([]ThreeXUIControllerMigrationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.kind, m.site_id, m.source_application_id, m.target_application_id, m.backup_revision, m.state, m.step, m.last_error, m.failed_worker_application_id, m.created_at, m.updated_at,
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
		if err := rows.Scan(&value.ID, &value.Kind, &value.SiteID, &value.SourceApplicationID, &value.TargetApplicationID, &value.BackupRevision, &value.State, &value.Step, &value.LastError, &value.FailedWorkerID, &created, &updated, &backupApplication, &backupRevision, &backupState, &backupSHA, &backupSize, &backupError, &backupUpdated); err != nil {
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
				var migrationKind string
				if err := tx.QueryRowContext(ctx, `SELECT kind FROM three_x_ui_migrations WHERE id = ?`, input.MigrationID).Scan(&migrationKind); err != nil {
					return err
				}
				var source, target struct {
					applicationID, siteID, agentID, role, status, name, address, lastSeen string
					panelPort, remoteNodeID                                               int
				}
				if err := s.loadThreeXUIMigrationEndpoints(ctx, tx, input.MigrationID, &source, &target); err != nil {
					return err
				}
				if migrationKind == "consolidate" {
					if err := s.queueThreeXUIConsolidationDemote(ctx, tx, input.MigrationID, source, target, now); err != nil {
						return err
					}
				} else if err := s.queueThreeXUIPromote(ctx, tx, input.MigrationID, source, target, input.BackupRevision, now); err != nil {
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
			var migrationKind string
			if err := tx.QueryRowContext(ctx, `SELECT kind FROM three_x_ui_migrations WHERE id = ?`, input.MigrationID).Scan(&migrationKind); err != nil {
				return err
			}
			if migrationKind == "consolidate" {
				if err := s.attachConsolidatedThreeXUIController(ctx, tx, input.MigrationID, now); err != nil {
					return err
				}
				message = "legacy 3x-ui controller backed up and queued as a VLESS node"
			} else {
				if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET step = 'switch', last_error = '', updated_at = ? WHERE id = ? AND state = 'switching'`, now.Format(time.RFC3339Nano), input.MigrationID); err != nil {
					return err
				}
				if err := s.queueNextThreeXUINodeAfterMigration(ctx, tx, input.MigrationID, now); err != nil {
					return err
				}
				message = "previous 3x-ui subscription host prepared to reconnect as a VLESS node"
			}
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id = ? AND kind = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, sourceApplicationID, controllerCommandKind).Scan(&active); err != nil {
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
	var targetSiteID, sourceAgentID, targetAgentID, targetAddress string
	var targetPanelPort, targetRemoteNodeID int
	if err := tx.QueryRowContext(ctx, `SELECT target.site_id, source.node_id, target.node_id, COALESCE(p.service_address, ''), COALESCE(json_extract(d.config_json, '$.panel_port'), 2053), topology.remote_node_id
		FROM applications source JOIN applications target ON target.id = ? JOIN agent_network_profiles p ON p.agent_id = target.node_id
		JOIN three_x_ui_nodes topology ON topology.worker_application_id = target.id AND topology.master_application_id = source.id
		JOIN deployments d ON d.rowid = (SELECT d2.rowid FROM deployments d2 WHERE d2.application_id = target.id AND d2.state = 'succeeded' AND d2.operation IN ('install','upgrade','configure') ORDER BY d2.updated_at DESC, d2.rowid DESC LIMIT 1)
		WHERE source.id = ? AND source.role = 'master' AND target.role = 'worker'`, input.ApplicationID, input.SourceApplicationID).Scan(&targetSiteID, &sourceAgentID, &targetAgentID, &targetAddress, &targetPanelPort, &targetRemoteNodeID); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE three_x_ui_control_plane SET controller_application_id = ?, selection_reason = 'migration:explicit-controller-replacement', selected_at = ? WHERE id = 1 AND controller_application_id = ?`, input.ApplicationID, now.Format(time.RFC3339Nano), input.SourceApplicationID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.New("center: global 3x-ui subscription controller changed during migration")
	}
	if err := s.copyApplicationSecrets(ctx, tx, input.SourceApplicationID, input.ApplicationID, now); err != nil {
		return err
	}
	if err := s.moveThreeXUICatalogServices(ctx, tx, input.SourceApplicationID, input.ApplicationID, targetSiteID, targetAddress, targetPanelPort, now); err != nil {
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
	return s.queueNextThreeXUINodeAfterMigration(ctx, tx, input.MigrationID, now)
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

func (s *Store) queueNextThreeXUINodeAfterMigration(ctx context.Context, tx *sql.Tx, migrationID string, now time.Time) error {
	var masterApplicationID, masterAgentID, sourceApplicationID, step string
	err := tx.QueryRowContext(ctx, `SELECT migration.target_application_id, master.node_id, migration.source_application_id, migration.step
		FROM three_x_ui_migrations migration JOIN applications master ON master.id = migration.target_application_id
		WHERE migration.id = ? AND migration.state = 'switching'`, migrationID).Scan(&masterApplicationID, &masterAgentID, &sourceApplicationID, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
		WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, masterAgentID, controllerCommandKind).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return nil
	}
	var workerApplicationID, deploymentID string
	err = tx.QueryRowContext(ctx, `SELECT node.worker_application_id, deployment.id
		FROM three_x_ui_nodes node
		JOIN applications worker ON worker.id = node.worker_application_id AND worker.status = 'running'
		JOIN deployments deployment ON deployment.rowid = (
			SELECT latest.rowid FROM deployments latest
			WHERE latest.application_id = node.worker_application_id AND latest.state = 'succeeded'
			AND latest.operation IN ('install', 'upgrade', 'configure')
			ORDER BY latest.updated_at DESC, latest.rowid DESC LIMIT 1
		)
		WHERE node.master_application_id = ? AND node.status = 'pending'
		AND (? = 'switch' OR node.worker_application_id <> ?)
		ORDER BY CASE WHEN node.worker_application_id = ? THEN 0 ELSE 1 END, node.created_at, node.worker_application_id
		LIMIT 1`, masterApplicationID, step, sourceApplicationID, sourceApplicationID).Scan(&workerApplicationID, &deploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.queueThreeXUINodeReconcile(ctx, tx, deploymentID, workerApplicationID, migrationID, now)
}
