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

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
)

const nodeListenerMigrationSetting = "migration_56_node_local_listener"

type nodeListenerPrerequisiteError struct{ cause error }

func (err nodeListenerPrerequisiteError) Error() string { return err.cause.Error() }
func (err nodeListenerPrerequisiteError) Unwrap() error { return err.cause }

func validateCenterNodeListenerState(state gateway.NodeListenerState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	for _, route := range state.Listener.Routes {
		if !route.ManagedReality {
			if route.ProxyProtocol == gateway.ProxyProtocolV2 {
				return errors.New("center: Proxy Protocol v2 is reserved for managed REALITY routes")
			}
			continue
		}
		if route.ProxyProtocol != gateway.ProxyProtocolV2 || len(route.Upstreams) != 1 || route.Upstreams[0].Address != dockerruntime.ThreeXUIAlias || route.Upstreams[0].Port != centerThreeXUIRealityPort {
			return errors.New("center: managed REALITY listener must target the local 3x-ui port 443 with Proxy Protocol v2")
		}
	}
	return nil
}

func (s *Store) queueNodeListenerState(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time) error {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM node_listener_states WHERE node_id = ?`, nodeID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	state, err := s.desiredNodeListenerState(ctx, tx, nodeID, current+1)
	if err != nil {
		return err
	}
	return s.saveNodeListenerState(ctx, tx, state, now, "node-local listener desired state queued")
}

func (s *Store) saveNodeListenerState(ctx context.Context, tx *sql.Tx, state gateway.NodeListenerState, now time.Time, message string) error {
	if err := validateCenterNodeListenerState(state); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_listener_states(node_id, desired_revision, applied_revision, desired_json, status, updated_at)
		VALUES(?, ?, 0, ?, 'pending', ?)
		ON CONFLICT(node_id) DO UPDATE SET desired_revision = excluded.desired_revision, desired_json = excluded.desired_json,
		status = excluded.status, lease_expires_at = '', last_error = '', updated_at = excluded.updated_at`, state.NodeID, state.Revision, payload, now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: queue node listener state: %w", err)
	}
	return s.recordTaskEvent(ctx, tx, nodeListenerTaskID(state.NodeID, state.Revision), state.NodeID, "node.listener.apply", state.Revision, "queued", message)
}

func (s *Store) claimNodeListenerTask(ctx context.Context, tx *sql.Tx, nodeID string) (*AgentTask, error) {
	var encoded []byte
	var revision, attempt int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, desired_json, attempt FROM node_listener_states
		WHERE node_id = ? AND desired_revision > applied_revision AND status IN ('pending', 'failed')`, nodeID).Scan(&revision, &encoded, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read node listener desired state: %w", err)
	}
	var state gateway.NodeListenerState
	if json.Unmarshal(encoded, &state) != nil || validateCenterNodeListenerState(state) != nil || state.NodeID != nodeID || state.Revision != revision {
		return nil, errors.New("center: invalid stored node listener desired state")
	}
	now := s.now().UTC()
	claimed, err := tx.ExecContext(ctx, `UPDATE node_listener_states SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ? WHERE node_id = ? AND desired_revision = ? AND attempt = ? AND status IN ('pending', 'failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nodeID, revision, attempt)
	if err != nil {
		return nil, err
	}
	if changed, _ := claimed.RowsAffected(); changed != 1 {
		return nil, errors.New("center: node listener task changed while claiming")
	}
	task := &AgentTask{Kind: "node.listener.apply", ID: nodeListenerTaskID(nodeID, revision), Attempt: attempt + 1, Revision: revision, NodeListenerState: &state}
	if err := s.recordTaskEvent(ctx, tx, task.ID, nodeID, task.Kind, task.Revision, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) desiredNodeListenerState(ctx context.Context, tx *sql.Tx, nodeID string, revision int64) (gateway.NodeListenerState, error) {
	routes := []gateway.Layer4Route{}
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.sni_hostname, s.endpoint, a.node_id, a.runtime, a.app_key, s.container_port,
		CASE WHEN a.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality' THEN 1 ELSE 0 END,
		CASE WHEN a.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality' AND g.status = 'ready' THEN 'v2' ELSE '' END
		FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id
		LEFT JOIN three_x_ui_reality_guards g ON g.service_id = s.id
		WHERE p.ingress_owner = 'application_node' AND p.entry_node_id = ? AND p.kind = 'public_shared_443' AND a.node_id = ?
		AND p.status <> 'stopped' AND s.status <> 'stopped' ORDER BY p.id`, nodeID, nodeID)
	if err != nil {
		return gateway.NodeListenerState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var route gateway.Layer4Route
		var endpoint, applicationNodeID, runtime, appKey string
		var containerPort, managedReality int
		if err := rows.Scan(&route.ID, &route.Hostname, &endpoint, &applicationNodeID, &runtime, &appKey, &containerPort, &managedReality, &route.ProxyProtocol); err != nil {
			return gateway.NodeListenerState{}, err
		}
		route.ManagedReality = managedReality != 0
		if applicationNodeID != nodeID {
			return gateway.NodeListenerState{}, errors.New("center: node-direct listener upstream belongs to another Agent")
		}
		route.ApplicationNodeID = applicationNodeID
		endpoint = canonicalGatewayServiceEndpoint(appKey, runtime, applicationNodeID, nodeID, containerPort, endpoint)
		host, portValue, err := net.SplitHostPort(endpoint)
		if err != nil {
			return gateway.NodeListenerState{}, errors.New("center: invalid node-direct listener endpoint")
		}
		port, _ := strconv.Atoi(portValue)
		if route.ManagedReality && (runtime != "docker" || host != dockerruntime.ThreeXUIAlias || port != centerThreeXUIRealityPort || route.ProxyProtocol != gateway.ProxyProtocolV2) {
			return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("managed REALITY listener requires its local 3x-ui port 443 with Proxy Protocol v2")}
		}
		route.Upstreams = []gateway.Upstream{{Address: host, Port: port}}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return gateway.NodeListenerState{}, err
	}
	if len(routes) == 0 {
		return gateway.NodeListenerState{
			Revision: revision,
			NodeID:   nodeID,
			Listener: gateway.SharedHTTPS{Address: "127.0.0.1", Port: 443, RejectUnmatched: true, Routes: routes},
		}.Sorted(), nil
	}
	var activeDocker int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active' AND json_extract(capabilities_json, '$.docker') = 1`, nodeID).Scan(&activeDocker); err != nil {
		return gateway.NodeListenerState{}, err
	}
	if activeDocker != 1 {
		return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("node-direct listener requires an active Docker Agent")}
	}
	_, bindAddress, err := validateNodeDirectPublicIngress(ctx, tx, nodeID)
	if err != nil {
		return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: err}
	}
	var siteGateway int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM site_gateways membership
		JOIN gateway_components component ON component.gateway_node_id = membership.agent_id
		WHERE membership.agent_id = ? AND component.desired_status = 'running' AND component.status = 'ready'
	)`, nodeID).Scan(&siteGateway); err != nil {
		return gateway.NodeListenerState{}, err
	}
	listener := gateway.SharedHTTPS{Address: bindAddress, Port: 443, RejectUnmatched: siteGateway == 0, Routes: routes}
	if siteGateway != 0 {
		listener.CaddyAddress, listener.CaddyPort = dockerruntime.CaddyAlias, 443
	}
	state := gateway.NodeListenerState{Revision: revision, NodeID: nodeID, Listener: listener}
	if err := validateCenterNodeListenerState(state); err != nil {
		return gateway.NodeListenerState{}, err
	}
	return state.Sorted(), nil
}

func (s *Store) activateMigratedNodeListeners(ctx context.Context) error {
	var marker string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || (marker != "pending" && marker != "applying") {
		return errors.New("center: node-listener migration marker is invalid")
	}
	if marker == "applying" {
		var cutoverTable int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'node_listener_migration_cutovers'`).Scan(&cutoverTable); err != nil || cutoverTable != 1 {
			return errors.New("center: node-listener migration cutover table is unavailable")
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT replacement_node_id FROM node_listener_migration_cutovers ORDER BY replacement_node_id`)
	if err != nil {
		return err
	}
	var nodes []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, nodeID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, nodeID := range nodes {
		now := s.now().UTC()
		if err := s.queueNodeListenerState(ctx, tx, nodeID, now); err != nil {
			var prerequisite nodeListenerPrerequisiteError
			if !errors.As(err, &prerequisite) {
				return err
			}
			message := "migration requires action: " + prerequisite.Error()
			if _, updateErr := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', action_required = 1, last_error = ?, updated_at = ?
				WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, message, now.Format(time.RFC3339Nano), nodeID); updateErr != nil {
				return updateErr
			}
			if _, updateErr := tx.ExecContext(ctx, `UPDATE node_listener_migration_cutovers SET listener_ready = 1, publication_ready = 1 WHERE replacement_node_id = ?`, nodeID); updateErr != nil {
				return updateErr
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_listener_migration_cutovers SET publication_ready = 1
		WHERE publication_id IN (SELECT id FROM publications WHERE status = 'stopped')`); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_migration_cutovers`).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if err := finishNodeListenerMigration(ctx, tx); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = 'applying' WHERE key = ? AND value = 'pending'`, nodeListenerMigrationSetting); err != nil {
			return err
		}
		if err := s.retireReadyLegacyGatewayCutovers(ctx, tx, s.now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) completeNodeListenerMigrationCutovers(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time) error {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || marker != "applying" {
		return errors.New("center: node-listener migration cutover state is invalid")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_listener_migration_cutovers
		SET listener_ready = 1,
		publication_ready = CASE WHEN publication_id IN (SELECT id FROM publications WHERE status = 'stopped') THEN 1 ELSE publication_ready END
		WHERE replacement_node_id = ?`, nodeID); err != nil {
		return err
	}
	return s.retireReadyLegacyGatewayCutovers(ctx, tx, now)
}

func (s *Store) completeNodeListenerMigrationPublication(ctx context.Context, tx *sql.Tx, publicationID string, now time.Time) error {
	var marker string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || marker != "applying" {
		return errors.New("center: node-listener migration cutover state is invalid")
	}
	result, err := tx.ExecContext(ctx, `UPDATE node_listener_migration_cutovers SET publication_ready = 1 WHERE publication_id = ?`, publicationID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil
	}
	return s.retireReadyLegacyGatewayCutovers(ctx, tx, now)
}

func (s *Store) nodeListenerMigrationDNSPending(ctx context.Context, publicationID string) (bool, error) {
	var marker string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || marker != "applying" {
		return false, errors.New("center: node-listener migration cutover state is invalid")
	}
	var reconciled int
	err = s.db.QueryRowContext(ctx, `SELECT dns_reconciled FROM node_listener_migration_cutovers WHERE publication_id = ?`, publicationID).Scan(&reconciled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return reconciled == 0, nil
}

func (s *Store) completeNodeListenerMigrationDNS(ctx context.Context, publicationID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE node_listener_migration_cutovers SET dns_reconciled = 1 WHERE publication_id = ?`, publicationID)
	return err
}

func (s *Store) retireReadyLegacyGatewayCutovers(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT candidate.legacy_gateway_id
		FROM node_listener_migration_cutovers candidate
		WHERE NOT EXISTS (
			SELECT 1 FROM node_listener_migration_cutovers pending
			WHERE pending.legacy_gateway_id = candidate.legacy_gateway_id
			AND (pending.listener_ready = 0 OR pending.publication_ready = 0)
		)
		ORDER BY candidate.legacy_gateway_id`)
	if err != nil {
		return err
	}
	var readyGateways []string
	for rows.Next() {
		var gatewayID string
		if err := rows.Scan(&gatewayID); err != nil {
			rows.Close()
			return err
		}
		readyGateways = append(readyGateways, gatewayID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, gatewayID := range readyGateways {
		var runningGateway int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_components WHERE gateway_node_id = ? AND desired_status = 'running')`, gatewayID).Scan(&runningGateway); err != nil {
			return err
		}
		if runningGateway != 0 {
			if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM node_listener_migration_cutovers WHERE legacy_gateway_id = ?`, gatewayID); err != nil {
			return err
		}
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_migration_cutovers`).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		return finishNodeListenerMigration(ctx, tx)
	}
	return nil
}

func finishNodeListenerMigration(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE node_listener_migration_cutovers`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, nodeListenerMigrationSetting)
	return err
}

func (s *Store) completeNodeListenerState(ctx context.Context, agentID string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
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
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM node_listener_states WHERE node_id = ?`, agentID).Scan(&desired, &applied, &status, &attempt); err != nil {
		return errors.New("center: node listener desired state not found")
	}
	if revision <= applied || revision < desired || (revision == desired && expectedAttempt < attempt) {
		return nil
	}
	if revision != desired || expectedAttempt > attempt {
		return errors.New("center: stale node listener result")
	}
	if status != "applying" {
		return nil
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var verificationTargets []publicationVerificationTarget
	if succeeded {
		_, err = tx.ExecContext(ctx, `UPDATE node_listener_states SET applied_revision = desired_revision, status = 'ready', lease_expires_at = '', last_error = '', updated_at = ? WHERE node_id = ?`, now, agentID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE publications SET applied_revision = desired_revision, status = 'applying', last_error = '', updated_at = ? WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, now, agentID)
		}
		if err == nil {
			verificationTargets, err = s.publicationVerificationTargetsForNodeListener(ctx, tx, agentID)
		}
		if err == nil {
			err = s.completeNodeListenerMigrationCutovers(ctx, tx, agentID, s.now().UTC())
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE node_listener_states SET status = 'failed', lease_expires_at = '', last_error = ?, updated_at = ? WHERE node_id = ?`, taskError, now, agentID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ? WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, taskError, now, agentID)
		}
	}
	if err != nil {
		return err
	}
	event := "succeeded"
	if !succeeded {
		event = "failed"
	}
	if err := s.recordTaskEvent(ctx, tx, nodeListenerTaskID(agentID, revision), agentID, "node.listener.apply", revision, event, taskError); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, target := range verificationTargets {
		s.schedulePublicationVerification(target.id, target.revision)
	}
	return nil
}

func (s *Store) queueMismatchedNodeListenerReconcile(ctx context.Context, tx *sql.Tx, nodeID string, healthy bool, liveRevision int64, liveHash string, now time.Time) error {
	var desiredRevision, appliedRevision int64
	var status string
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, desired_json FROM node_listener_states WHERE node_id = ?`, nodeID).Scan(&desiredRevision, &appliedRevision, &status, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("center: inspect live node-listener state: %w", err)
	}
	if status != "ready" || desiredRevision != appliedRevision {
		return nil
	}
	var desired gateway.NodeListenerState
	if json.Unmarshal(encoded, &desired) != nil || validateCenterNodeListenerState(desired) != nil || desired.NodeID != nodeID || desired.Revision != desiredRevision {
		return errors.New("center: stored node-listener desired state is invalid")
	}
	expectedHash, err := gateway.NodeListenerConfigurationHash(desired)
	if err != nil {
		return fmt.Errorf("center: hash expected node-listener configuration: %w", err)
	}
	if healthy && liveRevision == appliedRevision && liveHash == expectedHash {
		return nil
	}
	reason := "node-direct listener state differs from Center; queued for reconcile"
	if !healthy {
		reason = "node-direct listener health check failed; queued for reconcile"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'degraded', last_error = ?, updated_at = ?
		WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, reason, now.Format(time.RFC3339Nano), nodeID); err != nil {
		return fmt.Errorf("center: mark unhealthy node-direct publications: %w", err)
	}
	if err := s.queueNodeListenerState(ctx, tx, nodeID, now); err != nil {
		return fmt.Errorf("center: queue mismatched live node-listener state: %w", err)
	}
	return nil
}

func (s *Store) publicationVerificationTargetsForNodeListener(ctx context.Context, tx *sql.Tx, nodeID string) ([]publicationVerificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, desired_revision FROM publications WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status = 'applying'`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []publicationVerificationTarget
	for rows.Next() {
		var target publicationVerificationTarget
		if err := rows.Scan(&target.id, &target.revision); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func nodeListenerTaskID(nodeID string, revision int64) string {
	return fmt.Sprintf("node-listener-%s-r%d", nodeID, revision)
}

func nodeListenerTaskRevision(taskID string) (int64, bool) {
	marker := strings.LastIndex(taskID, "-r")
	if !strings.HasPrefix(taskID, "node-listener-") || marker < len("node-listener-") {
		return 0, false
	}
	revision, err := strconv.ParseInt(taskID[marker+2:], 10, 64)
	return revision, err == nil && revision > 0
}
