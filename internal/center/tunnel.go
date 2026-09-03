package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/secret"
)

const tunnelConnectorMigrationSetting = "migration_56_tunnel_connector"

func (s *Store) activateMigratedTunnelConnectors(ctx context.Context) error {
	var marker string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tunnelConnectorMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || (marker != "pending" && marker != "applying") {
		return errors.New("center: Tunnel connector migration marker is invalid")
	}
	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tunnel_connector_migration_cutovers'`).Scan(&tableExists); err != nil || tableExists != 1 {
		return errors.New("center: Tunnel connector migration cutover table is unavailable")
	}
	if marker == "applying" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tunnel_connector_migration_cutovers`).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if err := finishTunnelConnectorMigration(ctx, tx); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = 'applying' WHERE key = ? AND value = 'pending'`, tunnelConnectorMigrationSetting); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) tunnelConnectorMigrationReconcilePending(ctx context.Context, publicationID string) (bool, error) {
	var marker string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tunnelConnectorMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || marker != "applying" {
		return false, errors.New("center: Tunnel connector migration cutover state is invalid")
	}
	var reconciled int
	err = s.db.QueryRowContext(ctx, `SELECT connector_reconciled FROM tunnel_connector_migration_cutovers WHERE publication_id = ?`, publicationID).Scan(&reconciled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return reconciled == 0, nil
}

func (s *Store) completeTunnelConnectorMigrationReconcile(ctx context.Context, publicationID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tunnel_connector_migration_cutovers SET connector_reconciled = 1 WHERE publication_id = ?`, publicationID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Tunnel connector migration changed while reconciling")
	}
	return nil
}

func (s *Store) retireMigratedTunnelConnector(ctx context.Context, tx *sql.Tx, publicationID string, now time.Time) error {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tunnelConnectorMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || marker != "applying" {
		return errors.New("center: Tunnel connector migration cutover state is invalid")
	}
	var legacyGatewayID string
	err = tx.QueryRowContext(ctx, `SELECT legacy_gateway_id FROM tunnel_connector_migration_cutovers WHERE publication_id = ?`, publicationID).Scan(&legacyGatewayID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE publication_id = ?`, publicationID); err != nil {
		return err
	}
	var runningGateway int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_components WHERE gateway_node_id = ? AND desired_status = 'running')`, legacyGatewayID).Scan(&runningGateway); err != nil {
		return err
	}
	if runningGateway != 0 {
		if err := s.queueGatewayState(ctx, tx, legacyGatewayID, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tunnel_connector_migration_cutovers WHERE publication_id = ?`, publicationID); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tunnel_connector_migration_cutovers`).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		return finishTunnelConnectorMigration(ctx, tx)
	}
	return nil
}

func (s *Store) retireStoppedMigratedTunnelConnectors(ctx context.Context, tx *sql.Tx, applicationID string, now time.Time) error {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tunnelConnectorMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || marker != "applying" {
		return errors.New("center: Tunnel connector migration cutover state is invalid")
	}
	rows, err := tx.QueryContext(ctx, `SELECT cutover.publication_id
		FROM tunnel_connector_migration_cutovers cutover
		JOIN publications publication ON publication.id = cutover.publication_id
		JOIN services service ON service.id = publication.service_id
		WHERE service.application_id = ? AND publication.status = 'stopped'
		ORDER BY cutover.publication_id`, applicationID)
	if err != nil {
		return err
	}
	var publicationIDs []string
	for rows.Next() {
		var publicationID string
		if err := rows.Scan(&publicationID); err != nil {
			rows.Close()
			return err
		}
		publicationIDs = append(publicationIDs, publicationID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, publicationID := range publicationIDs {
		if err := s.retireMigratedTunnelConnector(ctx, tx, publicationID, now); err != nil {
			return err
		}
	}
	return nil
}

func finishTunnelConnectorMigration(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE tunnel_connector_migration_cutovers`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, tunnelConnectorMigrationSetting)
	return err
}

func tunnelIngressForNode(ctx context.Context, queryer networkQueryer, agentID string) ([]TunnelTaskIngress, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT p.hostname, s.protocol, s.endpoint, a.app_key, a.runtime, a.node_id, s.name, s.container_port
		FROM publications p
		JOIN services s ON s.id = p.service_id
		JOIN applications a ON a.id = s.application_id
		WHERE p.entry_node_id = ? AND p.ingress_owner = 'tunnel_connector' AND p.kind = 'cloudflare_tunnel'
		AND p.status <> 'stopped' AND s.status <> 'stopped'
		ORDER BY p.hostname`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ingress := []TunnelTaskIngress{}
	for rows.Next() {
		var value TunnelTaskIngress
		var protocol, endpoint, appKey, runtime, applicationNodeID, serviceName string
		var containerPort int
		if err := rows.Scan(&value.Hostname, &protocol, &endpoint, &appKey, &runtime, &applicationNodeID, &serviceName, &containerPort); err != nil {
			return nil, err
		}
		if protocol != "http" && protocol != "https" {
			return nil, errors.New("center: Tunnel connector received a non-Web service")
		}
		endpoint = canonicalGatewayServiceEndpoint(appKey, runtime, applicationNodeID, agentID, containerPort, endpoint)
		if _, _, err := net.SplitHostPort(endpoint); err != nil {
			return nil, errors.New("center: Tunnel connector service endpoint is invalid")
		}
		value.Service = protocol + "://" + endpoint
		if isCPAClientAPIService(appKey, serviceName) {
			value.Path = cpaClientAPITunnelPath
		}
		ingress = append(ingress, value)
	}
	return ingress, rows.Err()
}

func (s *Store) queueTunnelState(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) error {
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: configure Cloudflare before creating a Tunnel publication")
	} else if err != nil {
		return err
	}
	ingress, err := tunnelIngressForNode(ctx, tx, agentID)
	if err != nil {
		return err
	}
	status := "running"
	if len(ingress) == 0 {
		status = "stopped"
	}
	revision := current + 1
	state := TunnelTaskState{Revision: revision, Status: status, Image: deployapi.CloudflaredImage, Ingress: ingress}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET desired_revision = ?, desired_json = ?, status = 'pending', attempt = 0, lease_expires_at = '', last_error = '', updated_at = ? WHERE agent_id = ?`, revision, payload, now.Format(time.RFC3339Nano), agentID); err != nil {
		return fmt.Errorf("center: queue Tunnel state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET desired_revision = ?, status = 'pending', last_error = '', updated_at = ? WHERE ingress_owner = 'tunnel_connector' AND entry_node_id = ? AND kind = 'cloudflare_tunnel' AND status <> 'stopped'`, revision, now.Format(time.RFC3339Nano), agentID); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, tunnelTaskID(agentID, revision), agentID, "tunnel.state.apply", revision, "queued", "Cloudflare Tunnel desired state queued")
}

func (s *Store) claimTunnelTask(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var revision, attempt int64
	var desiredJSON []byte
	var tokenSecretID string
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, desired_json, token_secret_id, attempt FROM cloudflare_tunnels
		WHERE agent_id = ? AND desired_revision > applied_revision AND status IN ('pending', 'failed')`, agentID).Scan(&revision, &desiredJSON, &tokenSecretID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state TunnelTaskState
	if json.Unmarshal(desiredJSON, &state) != nil || state.Revision != revision || (state.Status != "running" && state.Status != "stopped") || strings.TrimSpace(state.Image) == "" {
		return nil, errors.New("center: invalid stored Tunnel desired state")
	}
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, tokenSecretID).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("center: read Tunnel token: %w", err)
	}
	token, err := secret.Open(s.key, sealed, []byte("cloudflare-tunnel:"+agentID))
	if err != nil {
		return nil, fmt.Errorf("center: decrypt Tunnel token: %w", err)
	}
	state.Token = string(token)
	now := s.now().UTC()
	claimed, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ?
		WHERE agent_id = ? AND desired_revision = ? AND attempt = ? AND status IN ('pending', 'failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, revision, attempt)
	if err != nil {
		return nil, err
	}
	if changed, _ := claimed.RowsAffected(); changed != 1 {
		return nil, errors.New("center: Tunnel desired state changed while claiming")
	}
	task := &AgentTask{Kind: "tunnel.state.apply", ID: tunnelTaskID(agentID, revision), Attempt: attempt + 1, Revision: revision, TunnelState: &state}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, revision, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeTunnelState(ctx context.Context, agentID string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desired, applied, attempt int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&desired, &applied, &status, &attempt); err != nil {
		return errors.New("center: Tunnel desired state not found")
	}
	if revision <= applied {
		return nil
	}
	if revision < desired || (revision == desired && expectedAttempt < attempt) {
		// The Agent completion outbox retries until Center acknowledges it. A
		// newer desired revision or claim already superseded this result, so
		// acknowledge the obsolete delivery without applying it. Rejecting it
		// here would permanently block the Agent from claiming newer tasks.
		return nil
	}
	if revision != desired || expectedAttempt > attempt {
		return errors.New("center: stale Tunnel result")
	}
	if status != "applying" {
		return nil
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	nextStatus, event := "ready", "succeeded"
	if !succeeded {
		nextStatus, event = "failed", "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET applied_revision = CASE WHEN ? THEN ? ELSE applied_revision END, status = ?, lease_expires_at = '', last_error = ?, updated_at = ? WHERE agent_id = ?`, succeeded, revision, nextStatus, taskError, now, agentID); err != nil {
		return err
	}
	publicationStatus := "ready"
	verificationTargets := []publicationVerificationTarget{}
	if succeeded {
		publicationStatus = "applying"
	}
	if !succeeded {
		publicationStatus = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET applied_revision = CASE WHEN ? THEN desired_revision ELSE applied_revision END, status = ?, last_error = ?, updated_at = ? WHERE ingress_owner = 'tunnel_connector' AND entry_node_id = ? AND kind = 'cloudflare_tunnel' AND desired_revision <= ? AND status <> 'stopped'`, succeeded, publicationStatus, taskError, now, agentID, revision); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, tunnelTaskID(agentID, revision), agentID, "tunnel.state.apply", revision, event, taskError); err != nil {
		return err
	}
	if succeeded {
		rows, err := tx.QueryContext(ctx, `SELECT id, desired_revision FROM publications
			WHERE ingress_owner = 'tunnel_connector' AND entry_node_id = ? AND kind = 'cloudflare_tunnel' AND desired_revision <= ? AND status <> 'stopped'`, agentID, revision)
		if err != nil {
			return err
		}
		for rows.Next() {
			var target publicationVerificationTarget
			if err := rows.Scan(&target.id, &target.revision); err != nil {
				rows.Close()
				return err
			}
			verificationTargets = append(verificationTargets, target)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, target := range verificationTargets {
		s.schedulePublicationVerification(target.id, target.revision)
	}
	return nil
}

func tunnelTaskID(agentID string, revision int64) string {
	return fmt.Sprintf("tunnel-%s-r%d", agentID, revision)
}

func tunnelTaskRevision(taskID string) (int64, bool) {
	marker := strings.LastIndex(taskID, "-r")
	if !strings.HasPrefix(taskID, "tunnel-") || marker <= len("tunnel-") {
		return 0, false
	}
	revision, err := strconv.ParseInt(taskID[marker+2:], 10, 64)
	return revision, err == nil && revision > 0
}
