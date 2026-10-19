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

	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
	"golang.org/x/mod/semver"
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
		AND NOT EXISTS (SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire','complete'))
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
	var meridianOwnsConvergence bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire','complete'))`).Scan(&meridianOwnsConvergence); err != nil {
		return err
	}
	if meridianOwnsConvergence {
		return tx.Commit()
	}
	// A stopped or uncertain execution is not permission to start an unrelated
	// controller conversion. Preserve its resources until explicit disposition.
	var unresolvedExecutions bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM task_executions execution
	 WHERE execution.disposition='' AND execution.state<>'succeeded' AND (
	  EXISTS(SELECT 1 FROM deployments deployment WHERE deployment.id=execution.task_id AND deployment.app_key=?) OR
	  EXISTS(SELECT 1 FROM application_commands command JOIN applications application ON application.id=command.application_id WHERE command.id=execution.task_id AND application.app_key=?)
	 ))`, threeXUIAppKey, threeXUIAppKey).Scan(&unresolvedExecutions); err != nil {
		return err
	}
	if unresolvedExecutions {
		return nil
	}

	var activeMigrations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching')`).Scan(&activeMigrations); err != nil {
		return err
	}
	if activeMigrations != 0 {
		if err := tx.Commit(); err != nil {
			return err
		}
		return s.resumeThreeXUIWorkerConversion(ctx)
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
	if err := requireLandingControllerTransferSafe(ctx, tx, sourceApplicationID, controllerApplicationID); err != nil {
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

// resumeThreeXUIWorkerConversion replaces a demoted legacy controller with the
// signed Xray-only worker runtime before topology reconciliation is allowed to
// continue. The role switch and the runtime replacement are deliberately two
// durable steps: a failed replacement leaves the migration recoverable and
// never makes a legacy 3x-ui container look like a ready worker.
func (s *Store) resumeThreeXUIWorkerConversion(ctx context.Context) error {
	var migrationID, applicationID, agentID, installedVersion string
	var config json.RawMessage
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT migration.id, source.id, source.node_id, deployment.app_version, deployment.config_json,
		(EXISTS(SELECT 1 FROM deployments current WHERE current.application_id=source.id AND (current.state IN ('pending','running') OR current.reconciliation_required=1)) OR
		EXISTS(SELECT 1 FROM application_commands current WHERE current.application_id=source.id AND (current.state IN ('pending','running') OR current.reconciliation_required=1)))
		FROM three_x_ui_migrations migration
		JOIN applications source ON source.id=migration.source_application_id AND source.role='worker'
		JOIN deployments deployment ON deployment.rowid=(
			SELECT latest.rowid FROM deployments latest WHERE latest.application_id=source.id
			AND latest.state='succeeded' AND latest.operation IN ('install','upgrade','configure')
			ORDER BY latest.updated_at DESC,latest.rowid DESC LIMIT 1
		)
		WHERE migration.state='switching' AND migration.step='convert_worker' AND migration.last_error=''
		ORDER BY migration.created_at,migration.rowid LIMIT 1`).Scan(&migrationID, &applicationID, &agentID, &installedVersion, &config, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if active != 0 {
		return nil
	}
	apps, err := s.ListApps(ctx)
	if err != nil {
		return s.failThreeXUIWorkerConversion(ctx, migrationID, applicationID, err)
	}
	var catalogVersion string
	for _, app := range apps {
		if app.Key == threeXUIAppKey {
			catalogVersion = app.App.Version
			break
		}
	}
	if catalogVersion == "" {
		return s.failThreeXUIWorkerConversion(ctx, migrationID, applicationID, errors.New("verified Vastora Proxy catalog entry is unavailable"))
	}
	operation := "configure"
	if comparison := semver.Compare(canonicalAppVersion(catalogVersion), canonicalAppVersion(installedVersion)); comparison < 0 {
		return s.failThreeXUIWorkerConversion(ctx, migrationID, applicationID, fmt.Errorf("catalog version %s is older than installed version %s", catalogVersion, installedVersion))
	} else if comparison > 0 {
		operation = "upgrade"
	}
	request := DeploymentRequest{AgentID: agentID, AppKey: threeXUIAppKey, Operation: operation, MigrationID: migrationID}
	if operation == "configure" {
		request.Config = config
	}
	if _, err := s.CreateDeployment(ctx, request); err != nil {
		// Another request may have won the race after the read above. Its
		// completion will resume this migration.
		if strings.Contains(err.Error(), "active deployment task") {
			return nil
		}
		return s.failThreeXUIWorkerConversion(ctx, migrationID, applicationID, err)
	}
	return nil
}

func (s *Store) failThreeXUIWorkerConversion(ctx context.Context, migrationID, applicationID string, cause error) error {
	message := controlplane.SafeError(cause.Error())
	_, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_migrations SET last_error=?,failed_worker_application_id=?,updated_at=?
		WHERE id=? AND state='switching' AND step='convert_worker'`, message, applicationID, s.now().UTC().Format(time.RFC3339Nano), migrationID)
	if err != nil {
		return err
	}
	return nil
}

// Shared-account host migration is outside the MVP. A native 3x-ui backup
// alone would copy partial budgets without their accounting state.
func requireLandingControllerTransferSafe(ctx context.Context, tx *sql.Tx, source, target string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_client_accounts p WHERE p.controller_id IN (?,?) AND
		(p.managed_quota=1 OR EXISTS(SELECT 1 FROM landing_client_grants WHERE parent_id=p.id))`, source, target).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("center: moving subscription hosts with landing combinations is not supported")
	}
	return nil
}

func (s *Store) CreateThreeXUIControllerMigration(ctx context.Context, sourceApplicationID string, input ThreeXUIControllerMigrationInput) (ThreeXUIControllerMigrationView, error) {
	return ThreeXUIControllerMigrationView{}, errors.New("center: legacy controller relocation is unavailable; use Meridian cutover")
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

// RetryThreeXUIControllerMigrationCleanup retries only the current cleanup
// boundary: either controller demotion or the subsequent Xray-only conversion.
// The new controller already owns subscriptions, so neither path replays the
// backup or restores stale data over the live copy.
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
	var sourceApplicationID, sourceAgentID, state, step, lastError string
	if err := tx.QueryRowContext(ctx, `SELECT m.source_application_id, source.node_id, m.state, m.step, m.last_error
		FROM three_x_ui_migrations m JOIN applications source ON source.id = m.source_application_id
		WHERE m.id = ?`, migrationID).Scan(&sourceApplicationID, &sourceAgentID, &state, &step, &lastError); errors.Is(err, sql.ErrNoRows) {
		return ThreeXUIControllerMigrationView{}, errors.New("center: 3x-ui controller migration not found")
	} else if err != nil {
		return ThreeXUIControllerMigrationView{}, err
	}
	if state != "switching" || lastError == "" {
		return ThreeXUIControllerMigrationView{}, errors.New("center: old 3x-ui controller cleanup does not need a retry")
	}
	var active int
	if step == "convert_worker" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`, sourceApplicationID).Scan(&active); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if active != 0 {
			return ThreeXUIControllerMigrationView{}, errors.New("center: Xray-only worker conversion is already running")
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET last_error='',failed_worker_application_id='',updated_at=? WHERE id=? AND state='switching' AND step='convert_worker'`, now.Format(time.RFC3339Nano), migrationID); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status='pending',last_error='',updated_at=? WHERE worker_application_id=?`, now.Format(time.RFC3339Nano), sourceApplicationID); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if err := tx.Commit(); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		if err := s.resumeThreeXUIWorkerConversion(ctx); err != nil {
			return ThreeXUIControllerMigrationView{}, err
		}
		return s.ThreeXUIControllerMigration(ctx, migrationID)
	}
	if step != "cleanup" {
		return ThreeXUIControllerMigrationView{}, errors.New("center: controller migration is not at a retryable cleanup boundary")
	}
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
