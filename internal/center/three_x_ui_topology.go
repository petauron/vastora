package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

const (
	threeXUIRoleMaster = "master"
	threeXUIRoleWorker = "worker"
)

var errThreeXUIAPISecretUnavailable = errors.New("center: 3x-ui node API token is unavailable")

type threeXUIControllerQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func runningGlobalThreeXUIController(ctx context.Context, query threeXUIControllerQuerier) (string, string, error) {
	var applicationID, agentID string
	err := query.QueryRowContext(ctx, `SELECT controller.id, controller.node_id
		FROM three_x_ui_control_plane control
		JOIN applications controller ON controller.id = control.controller_application_id
		WHERE control.id = 1 AND controller.app_key = ? AND controller.role = 'master'
		AND controller.status = 'running'`, threeXUIAppKey).Scan(&applicationID, &agentID)
	return applicationID, agentID, err
}

func threeXUIDataPlaneController(ctx context.Context, tx *sql.Tx, applicationID, role string) (string, int, error) {
	controllerApplicationID, controllerAgentID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return "", 0, errors.New("center: the global 3x-ui subscription controller is unavailable")
	}
	if role == threeXUIRoleMaster {
		if applicationID != controllerApplicationID {
			return "", 0, errors.New("center: legacy 3x-ui controller must be consolidated before it can manage VLESS")
		}
		return controllerAgentID, 0, nil
	}
	if role != threeXUIRoleWorker {
		return "", 0, errors.New("center: 3x-ui topology role is not configured")
	}
	var remoteNodeID int
	if err := tx.QueryRowContext(ctx, `SELECT node.remote_node_id
		FROM three_x_ui_nodes node
		WHERE node.worker_application_id = ? AND node.master_application_id = ?
		AND node.status = 'ready'`, applicationID, controllerApplicationID).Scan(&remoteNodeID); err != nil || remoteNodeID < 1 {
		return "", 0, errors.New("center: this VLESS node is not connected to the global 3x-ui subscription controller")
	}
	return controllerAgentID, remoteNodeID, nil
}

func (s *Store) validateThreeXUIInstallRole(ctx context.Context, agentID, role string) error {
	if role != threeXUIRoleMaster && role != threeXUIRoleWorker {
		return errors.New("center: choose whether this 3x-ui installation is the global subscription controller or a VLESS node")
	}
	var activeAgent int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active'`, strings.TrimSpace(agentID)).Scan(&activeAgent); err != nil || activeAgent != 1 {
		return errors.New("center: target node not found")
	}
	if role == threeXUIRoleMaster {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE app_key = ? AND role = 'master' AND status IN ('pending', 'deploying', 'running')`, threeXUIAppKey).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("center: this Center already has a 3x-ui subscription controller")
		}
		return nil
	}
	if _, _, err := runningGlobalThreeXUIController(ctx, s.db); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: install the global 3x-ui subscription controller before adding a VLESS node")
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) enforceThreeXUIWorkerIsolation(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.application_id FROM services s
		JOIN applications a ON a.id = s.application_id
		WHERE a.app_key = ? AND a.role = 'worker' AND s.source = 'catalog' AND s.status <> 'stopped'
		AND NOT EXISTS (
			SELECT 1 FROM three_x_ui_migrations migration
			WHERE migration.kind = 'consolidate' AND migration.source_application_id = a.id
			AND migration.state IN ('backing_up', 'restoring', 'switching')
		)`, threeXUIAppKey)
	if err != nil {
		return err
	}
	type origin struct{ serviceID, applicationID string }
	origins := []origin{}
	for rows.Next() {
		var value origin
		if err := rows.Scan(&value.serviceID, &value.applicationID); err != nil {
			rows.Close()
			return err
		}
		origins = append(origins, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(origins) == 0 {
		return tx.Commit()
	}
	now := s.now().UTC()
	cleanups := []publicationCleanup{}
	gateways, tunnels := map[string]bool{}, map[string]bool{}
	for _, value := range origins {
		values, err := s.servicePublicationCleanups(ctx, tx, value.serviceID)
		if err != nil {
			return err
		}
		cleanups = append(cleanups, values...)
		for _, publication := range values {
			if publication.Kind == publicationCloudflare && publication.GatewayID != "" {
				tunnels[publication.GatewayID] = true
			} else if publication.GatewayID != "" {
				gateways[publication.GatewayID] = true
			}
		}
		routeRows, err := tx.QueryContext(ctx, `SELECT DISTINCT gateway_node_id FROM routes WHERE service_id = ?`, value.serviceID)
		if err != nil {
			return err
		}
		for routeRows.Next() {
			var gatewayID string
			if err := routeRows.Scan(&gatewayID); err != nil {
				routeRows.Close()
				return err
			}
			gateways[gatewayID] = true
		}
		if err := routeRows.Close(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
			cleanup_pending = CASE WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
			cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE service_id = ? AND status <> 'stopped'`, now.Format(time.RFC3339Nano), value.serviceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE service_id = ?`, value.serviceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), value.serviceID); err != nil {
			return err
		}
	}
	for gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	for tunnelID := range tunnels {
		if err := s.queueTunnelState(ctx, tx, tunnelID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// External DNS/Tunnel cleanup is retryable and records its own failure state;
	// a temporary provider outage must not prevent Center from starting.
	_ = s.cleanupStoppedPublications(ctx, cleanups)
	return nil
}

func (s *Store) stopThreeXUICatalogServices(ctx context.Context, tx *sql.Tx, applicationID, formattedNow string) ([]publicationCleanup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM services WHERE application_id = ? AND source = 'catalog' AND status <> 'stopped' ORDER BY id`, applicationID)
	if err != nil {
		return nil, err
	}
	serviceIDs := []string{}
	for rows.Next() {
		var serviceID string
		if err := rows.Scan(&serviceID); err != nil {
			rows.Close()
			return nil, err
		}
		serviceIDs = append(serviceIDs, serviceID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	parsedNow, err := time.Parse(time.RFC3339Nano, formattedNow)
	if err != nil {
		return nil, err
	}
	cleanups := []publicationCleanup{}
	gateways, tunnels := map[string]bool{}, map[string]bool{}
	for _, serviceID := range serviceIDs {
		values, err := s.servicePublicationCleanups(ctx, tx, serviceID)
		if err != nil {
			return nil, err
		}
		cleanups = append(cleanups, values...)
		for _, publication := range values {
			if publication.Kind == publicationCloudflare && publication.GatewayID != "" {
				tunnels[publication.GatewayID] = true
			} else if publication.GatewayID != "" {
				gateways[publication.GatewayID] = true
			}
		}
		routeRows, err := tx.QueryContext(ctx, `SELECT DISTINCT gateway_node_id FROM routes WHERE service_id = ?`, serviceID)
		if err != nil {
			return nil, err
		}
		for routeRows.Next() {
			var gatewayID string
			if err := routeRows.Scan(&gatewayID); err != nil {
				routeRows.Close()
				return nil, err
			}
			gateways[gatewayID] = true
		}
		if err := routeRows.Close(); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
			cleanup_pending = CASE WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
			cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE service_id = ? AND status <> 'stopped'`, formattedNow, serviceID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE service_id = ?`, serviceID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', last_error = '', updated_at = ? WHERE id = ?`, formattedNow, serviceID); err != nil {
			return nil, err
		}
	}
	for gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, parsedNow); err != nil {
			return nil, err
		}
	}
	for tunnelID := range tunnels {
		if err := s.queueTunnelState(ctx, tx, tunnelID, parsedNow); err != nil {
			return nil, err
		}
	}
	return cleanups, nil
}

func (s *Store) validateThreeXUIUninstall(ctx context.Context, agentID string) error {
	var applicationID, role string
	if err := s.db.QueryRowContext(ctx, `SELECT id, role FROM applications WHERE node_id = ? AND app_key = ? AND status <> 'stopped'`, strings.TrimSpace(agentID), threeXUIAppKey).Scan(&applicationID, &role); err != nil {
		return nil
	}
	if role != threeXUIRoleMaster {
		return nil
	}
	var workers int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_nodes n JOIN applications a ON a.id = n.worker_application_id
		WHERE n.master_application_id = ? AND (a.status <> 'stopped' OR n.status IN ('pending', 'applying'))`, applicationID).Scan(&workers); err != nil {
		return err
	}
	if workers != 0 {
		return errors.New("center: migrate or remove the global VLESS nodes before uninstalling the 3x-ui subscription controller")
	}
	return nil
}

func (s *Store) queueThreeXUINodeReconcile(ctx context.Context, tx *sql.Tx, deploymentID, workerApplicationID, migrationID string, now time.Time) error {
	var role, workerName, address, masterApplicationID, masterAgentID string
	var configJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT a.role, ag.name,
		d.service_address, d.config_json
		FROM applications a
		JOIN agents ag ON ag.id = a.node_id
		JOIN deployments d ON d.id = ? AND d.application_id = a.id
		WHERE a.id = ?`, deploymentID, workerApplicationID).Scan(&role, &workerName, &address, &configJSON); err != nil {
		return fmt.Errorf("center: read 3x-ui node topology: %w", err)
	}
	if role != threeXUIRoleWorker {
		return nil
	}
	if net.ParseIP(address) == nil {
		return errors.New("center: VLESS node needs a confirmed Headscale or private service address")
	}
	var config struct {
		PanelPort int `json:"panel_port"`
	}
	if json.Unmarshal(configJSON, &config) != nil || config.PanelPort < 1024 || config.PanelPort > 65535 {
		return errors.New("center: stored 3x-ui node configuration is invalid")
	}
	if masterApplicationID, masterAgentID, err = runningGlobalThreeXUIController(ctx, tx); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: the global 3x-ui subscription controller is unavailable")
	} else if err != nil {
		return err
	}
	var remoteNodeID sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT remote_node_id FROM three_x_ui_nodes WHERE worker_application_id = ?`, workerApplicationID).Scan(&remoteNodeID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id, master_application_id, remote_node_id, status, last_error, created_at, updated_at)
		VALUES(?, ?, ?, 'pending', '', ?, ?)
		ON CONFLICT(worker_application_id) DO UPDATE SET master_application_id = excluded.master_application_id,
		remote_node_id = COALESCE(three_x_ui_nodes.remote_node_id, excluded.remote_node_id), status = 'pending', last_error = '', updated_at = excluded.updated_at`,
		workerApplicationID, masterApplicationID, nullableInt64(remoteNodeID), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: save 3x-ui node topology: %w", err)
	}
	task := ThreeXUINodeCommandTask{
		Action: "reconcile", MigrationID: migrationID, WorkerApplicationID: workerApplicationID,
		Name: workerName, Address: address, Port: config.PanelPort,
	}
	if remoteNodeID.Valid {
		task.RemoteNodeID = int(remoteNodeID.Int64)
	}
	return s.insertThreeXUINodeCommand(ctx, tx, masterAgentID, workerApplicationID, task, now)
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func (s *Store) queueThreeXUINodeRemoval(ctx context.Context, tx *sql.Tx, workerApplicationID string, now time.Time) error {
	var masterAgentID string
	var remoteNodeID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT master.node_id, n.remote_node_id
		FROM three_x_ui_nodes n JOIN applications master ON master.id = n.master_application_id
		WHERE n.worker_application_id = ?`, workerApplicationID).Scan(&masterAgentID, &remoteNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !remoteNodeID.Valid || remoteNodeID.Int64 < 1 {
		_, err = tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'stopped', last_error = '', updated_at = ? WHERE worker_application_id = ?`, now.Format(time.RFC3339Nano), workerApplicationID)
		return err
	}
	task := ThreeXUINodeCommandTask{Action: "remove", WorkerApplicationID: workerApplicationID, RemoteNodeID: int(remoteNodeID.Int64)}
	return s.insertThreeXUINodeCommand(ctx, tx, masterAgentID, workerApplicationID, task, now)
}

func (s *Store) insertThreeXUINodeCommand(ctx context.Context, tx *sql.Tx, masterAgentID, workerApplicationID string, task ThreeXUINodeCommandTask, now time.Time) error {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, masterAgentID, controllerCommandKind).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: this VLESS node already has an operation in progress")
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		return err
	}
	token, err := randomToken(18)
	if err != nil {
		return err
	}
	id := "application-command-" + token
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, workerApplicationID, masterAgentID, masterAgentID, nodeCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: queue 3x-ui node operation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'pending', last_error = '', updated_at = ? WHERE worker_application_id = ?`, now.Format(time.RFC3339Nano), workerApplicationID); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, id, masterAgentID, "application.command", 1, "queued", "3x-ui VLESS node synchronization queued")
}

func (s *Store) threeXUIAPISecret(ctx context.Context, tx *sql.Tx, applicationID string) (string, error) {
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT s.sealed FROM application_secrets a JOIN secrets s ON s.id = a.secret_id WHERE a.application_id = ?`, applicationID).Scan(&sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errThreeXUIAPISecretUnavailable
		}
		return "", err
	}
	plain, err := secret.Open(s.key, sealed, []byte("application:"+applicationID))
	if err != nil {
		return "", fmt.Errorf("%w: stored value cannot be decrypted", errThreeXUIAPISecretUnavailable)
	}
	var values map[string]string
	if json.Unmarshal(plain, &values) != nil || strings.TrimSpace(values["api_token"]) == "" {
		return "", fmt.Errorf("%w: stored value is invalid", errThreeXUIAPISecretUnavailable)
	}
	return strings.TrimSpace(values["api_token"]), nil
}

func (s *Store) ReconcileThreeXUINode(ctx context.Context, applicationID string) (ApplicationCommandView, error) {
	applicationID = strings.TrimSpace(applicationID)
	if applicationID == "" {
		return ApplicationCommandView{}, errors.New("center: application id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var deploymentID string
	if err := tx.QueryRowContext(ctx, `SELECT d.id FROM deployments d
		JOIN applications a ON a.id = d.application_id
		WHERE a.id = ? AND a.app_key = ? AND a.role = 'worker' AND a.status = 'running'
		AND d.rowid = (
			SELECT latest.rowid FROM deployments latest
			WHERE latest.application_id = a.id AND latest.state = 'succeeded'
			AND latest.operation IN ('install', 'upgrade', 'configure')
			ORDER BY latest.updated_at DESC, latest.rowid DESC LIMIT 1
		)`, applicationID, threeXUIAppKey).Scan(&deploymentID); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: running VLESS node installation was not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	var migrationID string
	if err := tx.QueryRowContext(ctx, `SELECT m.id FROM three_x_ui_nodes n
		JOIN three_x_ui_migrations m ON m.target_application_id = n.master_application_id
		WHERE n.worker_application_id = ? AND m.state = 'switching'
		AND (m.source_application_id <> ? OR m.step = 'switch')`, applicationID, applicationID).Scan(&migrationID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, err
	}
	now := s.now().UTC()
	if err := s.queueThreeXUINodeReconcile(ctx, tx, deploymentID, applicationID, migrationID, now); err != nil {
		return ApplicationCommandView{}, err
	}
	var commandID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM application_commands WHERE application_id = ? AND kind = ? ORDER BY created_at DESC LIMIT 1`, applicationID, nodeCommandKind).Scan(&commandID); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, commandID)
}

func (s *Store) completeThreeXUINodeCommand(ctx context.Context, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input ThreeXUINodeCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.WorkerApplicationID == "" || (input.Action != "reconcile" && input.Action != "remove") {
		return errors.New("center: stored 3x-ui node operation is invalid")
	}
	var envelope ApplicationTaskResult
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.NodeCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid 3x-ui node result"
		}
	}
	if succeeded {
		result := *envelope.NodeCommand
		if input.Action == "reconcile" && (result.RemoteNodeID < 1 || result.Status != "ready") {
			succeeded = false
			taskError = "center: 3x-ui controller did not confirm the VLESS node"
		} else if input.Action == "remove" && (result.RemoteNodeID != input.RemoteNodeID || result.Status != "stopped") {
			succeeded = false
			taskError = "center: 3x-ui controller did not confirm VLESS node removal"
		}
	}
	if taskError == "" && !succeeded {
		taskError = "3x-ui VLESS node synchronization failed"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, event, message := "succeeded", "succeeded", "3x-ui VLESS node is connected"
	resultJSON := []byte(`{}`)
	if succeeded {
		resultJSON, _ = json.Marshal(envelope.NodeCommand)
		if input.Action == "remove" {
			message = "3x-ui VLESS node was removed"
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'stopped', last_error = '', updated_at = ? WHERE worker_application_id = ?`, now, input.WorkerApplicationID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET remote_node_id = ?, status = 'ready', last_error = '', updated_at = ? WHERE worker_application_id = ?`, envelope.NodeCommand.RemoteNodeID, now, input.WorkerApplicationID); err != nil {
			return err
		}
	} else {
		state, event, message = "failed", "failed", taskError
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'failed', last_error = ?, updated_at = ? WHERE worker_application_id = ?`, taskError, now, input.WorkerApplicationID); err != nil {
			return err
		}
		if input.MigrationID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET last_error = ?, failed_worker_application_id = ?, updated_at = ?
					WHERE id = ? AND state = 'switching'`, taskError, input.WorkerApplicationID, now, input.MigrationID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	if succeeded && input.Action == "reconcile" && input.MigrationID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET last_error = '', failed_worker_application_id = '', updated_at = ? WHERE id = ? AND state = 'switching'`, now, input.MigrationID); err != nil {
			return err
		}
		if err := s.queueNextThreeXUINodeAfterMigration(ctx, tx, input.MigrationID, s.now().UTC()); err != nil {
			return err
		}
	}
	cleanups := []publicationCleanup{}
	if succeeded && input.Action == "reconcile" && input.MigrationID != "" {
		var err error
		cleanups, err = s.completeThreeXUIControllerMigrationIfReady(ctx, tx, input.MigrationID, now)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(cleanups) != 0 {
		_ = s.cleanupStoppedPublications(context.WithoutCancel(ctx), cleanups)
	}
	if input.MigrationID != "" {
		s.startBackground(func() { _ = s.resumeThreeXUIControllerConvergence(s.backgroundCtx) })
	}
	return nil
}

func (s *Store) completeThreeXUIControllerMigrationIfReady(ctx context.Context, tx *sql.Tx, migrationID, now string) ([]publicationCleanup, error) {
	var migrationKind, sourceApplicationID, masterApplicationID string
	err := tx.QueryRowContext(ctx, `SELECT kind, source_application_id, target_application_id
		FROM three_x_ui_migrations WHERE id = ? AND state = 'switching'`, migrationID).Scan(&migrationKind, &sourceApplicationID, &masterApplicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var unfinished int
	if migrationKind == "consolidate" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_nodes WHERE master_application_id = ? AND status IN ('pending', 'applying')`, masterApplicationID).Scan(&unfinished); err != nil {
			return nil, err
		}
	} else if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_nodes WHERE master_application_id = ? AND status <> 'ready'`, masterApplicationID).Scan(&unfinished); err != nil {
		return nil, err
	}
	if unfinished != 0 {
		return nil, nil
	}
	cleanups := []publicationCleanup{}
	if migrationKind == "consolidate" {
		cleanups, err = s.stopThreeXUICatalogServices(ctx, tx, sourceApplicationID, now)
		if err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'ready', step = 'complete', last_error = '', updated_at = ? WHERE id = ? AND state = 'switching'`, now, migrationID)
	return cleanups, err
}
