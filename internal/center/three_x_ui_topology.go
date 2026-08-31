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

func (s *Store) validateThreeXUIInstallRole(ctx context.Context, agentID, role string) error {
	if role != threeXUIRoleMaster && role != threeXUIRoleWorker {
		return errors.New("center: choose whether this 3x-ui installation is the Site controller or a VLESS node")
	}
	var siteID string
	if err := s.db.QueryRowContext(ctx, `SELECT site_id FROM agents WHERE id = ? AND status = 'active'`, strings.TrimSpace(agentID)).Scan(&siteID); err != nil {
		return errors.New("center: target node not found")
	}
	if role == threeXUIRoleMaster {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE site_id = ? AND app_key = ? AND role = 'master' AND status IN ('pending', 'deploying', 'running')`, siteID, threeXUIAppKey).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("center: this Site already has a 3x-ui controller")
		}
		return nil
	}
	var masterID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM applications WHERE site_id = ? AND app_key = ? AND role = 'master' AND status = 'running'`, siteID, threeXUIAppKey).Scan(&masterID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: install the Site 3x-ui controller before adding a VLESS node")
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
		WHERE a.app_key = ? AND a.role = 'worker' AND s.source = 'catalog' AND s.status <> 'stopped'`, threeXUIAppKey)
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
		return errors.New("center: remove this Site's VLESS nodes before uninstalling its 3x-ui controller")
	}
	return nil
}

func (s *Store) queueThreeXUINodeReconcile(ctx context.Context, tx *sql.Tx, deploymentID, workerApplicationID, migrationID string, now time.Time) error {
	var role, siteID, workerName, address, masterApplicationID, masterAgentID string
	var configJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT a.role, a.site_id, ag.name,
		d.service_address, d.config_json
		FROM applications a
		JOIN agents ag ON ag.id = a.node_id
		JOIN deployments d ON d.id = ? AND d.application_id = a.id
		WHERE a.id = ?`, deploymentID, workerApplicationID).Scan(&role, &siteID, &workerName, &address, &configJSON); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT id, node_id FROM applications
		WHERE site_id = ? AND app_key = ? AND role = 'master' AND status = 'running'`, siteID, threeXUIAppKey).Scan(&masterApplicationID, &masterAgentID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: this Site has no running 3x-ui controller")
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
		WHERE a.id = ? AND a.app_key = ? AND a.role = 'worker' AND a.status = 'running' AND d.operation = 'install' AND d.state = 'succeeded'
		ORDER BY d.updated_at DESC LIMIT 1`, applicationID, threeXUIAppKey).Scan(&deploymentID); errors.Is(err, sql.ErrNoRows) {
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
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET last_error = ?, updated_at = ?
			WHERE state = 'switching' AND target_application_id = (SELECT master_application_id FROM three_x_ui_nodes WHERE worker_application_id = ?)`, taskError, now, input.WorkerApplicationID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	if input.Action == "reconcile" && input.MigrationID != "" {
		if err := s.queueNextThreeXUINodeAfterMigration(ctx, tx, input.MigrationID, s.now().UTC()); err != nil {
			return err
		}
	}
	if succeeded && input.Action == "reconcile" {
		if err := s.completeThreeXUIControllerMigrationIfReady(ctx, tx, input.WorkerApplicationID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) completeThreeXUIControllerMigrationIfReady(ctx context.Context, tx *sql.Tx, workerApplicationID, now string) error {
	var migrationID, masterApplicationID string
	err := tx.QueryRowContext(ctx, `SELECT m.id, n.master_application_id
		FROM three_x_ui_nodes n JOIN three_x_ui_migrations m ON m.target_application_id = n.master_application_id
		WHERE n.worker_application_id = ? AND m.state = 'switching'`, workerApplicationID).Scan(&migrationID, &masterApplicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var unfinished int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_nodes WHERE master_application_id = ? AND status <> 'ready'`, masterApplicationID).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished != 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'ready', step = 'complete', last_error = '', updated_at = ? WHERE id = ? AND state = 'switching'`, now, migrationID)
	return err
}
