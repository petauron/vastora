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

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
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
			if route.ProxyProtocol == gateway.ProxyProtocolV2 && (len(route.Upstreams) != 1 || !networking.IsPrivateServiceAddress(route.Upstreams[0].Address) || net.ParseIP(route.Upstreams[0].Address).IsLoopback()) {
				return errors.New("center: service forwarding requires one private backend")
			}
			continue
		}
		upstreamOK := (len(route.Upstreams) == 1 && route.Upstreams[0].Port == centerThreeXUIRealityPort && (route.Upstreams[0].Address == dockerruntime.LegacyGlivelaAlias || route.Upstreams[0].Address == dockerruntime.LegacyXrayAlias || route.Upstreams[0].Address == dockerruntime.ThreeXUIAlias)) || len(route.Upstreams) == 1 && route.Upstreams[0].Port == glivela.RealityBackendPort && networking.IsPrivateServiceAddress(route.Upstreams[0].Address)
		if route.ProxyProtocol != gateway.ProxyProtocolV2 || !upstreamOK {
			return errors.New("center: managed REALITY listener must target the approved managed backend with Proxy Protocol v2")
		}
	}
	return nil
}

var (
	errNodeListenerNotFound = errors.New("center: node has no managed shared listener")
	errNodeListenerBusy     = errors.New("center: wait for the current node listener operation to finish")
	errNodeListenerFailed   = errors.New("center: resolve the failed node listener task before recomputing")
)

type NodeListenerReconcileResult struct {
	AgentID         string `json:"agentId"`
	Revision        int64  `json:"revision"`
	Queued          bool   `json:"queued"`
	Status          string `json:"status"`
	RejectUnmatched bool   `json:"rejectUnmatched"`
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

func nodeListenerSpecEqual(first, second gateway.NodeListenerState) (bool, error) {
	first.Revision = 1
	second.Revision = 1
	firstJSON, err := json.Marshal(first.Sorted())
	if err != nil {
		return false, err
	}
	secondJSON, err := json.Marshal(second.Sorted())
	if err != nil {
		return false, err
	}
	return string(firstJSON) == string(secondJSON), nil
}

// syncNodeListenerStateIfChanged recomputes an existing node-direct listener
// after a fallback dependency changes and queues a new revision only when the
// effective listener specification changed.
func (s *Store) syncNodeListenerStateIfChanged(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time, message string) (bool, error) {
	var currentRevision int64
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, desired_json FROM node_listener_states WHERE node_id = ?`, nodeID).Scan(&currentRevision, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	blocked, err := agentReinstallBlocked(ctx, tx, nodeID)
	if err != nil || blocked {
		return false, err
	}
	var current gateway.NodeListenerState
	if json.Unmarshal(encoded, &current) != nil || validateCenterNodeListenerState(current) != nil || current.NodeID != nodeID || current.Revision != currentRevision {
		return false, errors.New("center: stored node-listener desired state is invalid")
	}
	next, err := s.desiredNodeListenerState(ctx, tx, nodeID, currentRevision+1)
	if err != nil {
		var prerequisite nodeListenerPrerequisiteError
		if errors.As(err, &prerequisite) {
			return false, nil
		}
		return false, err
	}
	equal, err := nodeListenerSpecEqual(current, next)
	if err != nil || equal {
		return false, err
	}
	if err := s.saveNodeListenerState(ctx, tx, next, now, message); err != nil {
		return false, err
	}
	return true, nil
}

// ReconcileNodeListener recomputes one Agent's node-direct shared 443 listener
// on explicit operator request and queues a new revision only if the effective
// listener specification changed. Other nodes and unchanged listeners stay untouched.
func (s *Store) ReconcileNodeListener(ctx context.Context, agentID string) (NodeListenerReconcileResult, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return NodeListenerReconcileResult{}, errors.New("center: agent is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeListenerReconcileResult{}, err
	}
	defer tx.Rollback()
	var agentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agents WHERE id = ?`, agentID).Scan(&agentStatus); errors.Is(err, sql.ErrNoRows) {
		return NodeListenerReconcileResult{}, errors.New("center: active Agent not found")
	} else if err != nil {
		return NodeListenerReconcileResult{}, err
	}
	if agentStatus != "active" {
		return NodeListenerReconcileResult{}, errors.New("center: active Agent not found")
	}
	if blocked, err := agentReinstallBlocked(ctx, tx, agentID); err != nil {
		return NodeListenerReconcileResult{}, err
	} else if blocked {
		return NodeListenerReconcileResult{}, errExecutionBlocked
	}
	var unresolved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM task_executions
		WHERE agent_id = ? AND state <> 'succeeded' AND disposition = ''
	)`, agentID).Scan(&unresolved); err != nil {
		return NodeListenerReconcileResult{}, err
	}
	if unresolved {
		return NodeListenerReconcileResult{}, errExecutionBlocked
	}
	var desiredRevision, appliedRevision int64
	var status string
	var encoded []byte
	err = tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, desired_json FROM node_listener_states WHERE node_id = ?`, agentID).Scan(&desiredRevision, &appliedRevision, &status, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeListenerReconcileResult{}, errNodeListenerNotFound
	}
	if err != nil {
		return NodeListenerReconcileResult{}, err
	}
	var current gateway.NodeListenerState
	if json.Unmarshal(encoded, &current) != nil || validateCenterNodeListenerState(current) != nil || current.NodeID != agentID || current.Revision != desiredRevision {
		return NodeListenerReconcileResult{}, errors.New("center: stored node-listener desired state is invalid")
	}
	next, err := s.desiredNodeListenerState(ctx, tx, agentID, desiredRevision+1)
	if err != nil {
		return NodeListenerReconcileResult{}, err
	}
	equal, err := nodeListenerSpecEqual(current, next)
	if err != nil {
		return NodeListenerReconcileResult{}, err
	}
	if equal {
		if status == "failed" {
			return NodeListenerReconcileResult{}, errNodeListenerFailed
		}
		return NodeListenerReconcileResult{
			AgentID:         agentID,
			Revision:        desiredRevision,
			Queued:          false,
			Status:          status,
			RejectUnmatched: current.Listener.RejectUnmatched,
		}, nil
	}
	if status == "failed" {
		return NodeListenerReconcileResult{}, errNodeListenerFailed
	}
	if status != "ready" || desiredRevision != appliedRevision {
		return NodeListenerReconcileResult{}, errNodeListenerBusy
	}
	if err := s.saveNodeListenerState(ctx, tx, next, s.now().UTC(), "node-local listener desired state recomputed"); err != nil {
		return NodeListenerReconcileResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return NodeListenerReconcileResult{}, err
	}
	return NodeListenerReconcileResult{
		AgentID:         agentID,
		Revision:        next.Revision,
		Queued:          true,
		Status:          "pending",
		RejectUnmatched: next.Listener.RejectUnmatched,
	}, nil
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
		WHERE node_id = ? AND desired_revision > applied_revision AND status = 'pending'`, nodeID).Scan(&revision, &encoded, &attempt)
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
	claimed, err := tx.ExecContext(ctx, `UPDATE node_listener_states SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ? WHERE node_id = ? AND desired_revision = ? AND attempt = ? AND status = 'pending'`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nodeID, revision, attempt)
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
	return s.buildNodeListenerState(ctx, tx, nodeID, revision, nil)
}

// A recovering host has no active profile or published service binding yet.
// Only the reviewed, restored Glivela backend may enter its fresh listener.
type nodeListenerRestoreTarget struct {
	ApplicationID, PreviousBackendAddress, BackendAddress, BindAddress string
}

func (s *Store) buildNodeListenerState(ctx context.Context, tx *sql.Tx, nodeID string, revision int64, restore *nodeListenerRestoreTarget) (gateway.NodeListenerState, error) {
	routes := []gateway.Layer4Route{}
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.sni_hostname, s.endpoint, a.id, a.node_id, a.runtime, a.role, a.app_key, a.runtime_generation, s.container_port,p.action_required OR p.cleanup_pending,
		CASE WHEN (a.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality') OR (a.app_key = 'vastora-official/meridian' AND s.app_protocol = 'meridian/entry') THEN 1 ELSE 0 END,
		CASE WHEN a.app_key = 'vastora-official/meridian' AND s.app_protocol = 'meridian/entry' THEN 'v2'
		     WHEN a.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality' AND g.status = 'ready' THEN 'v2' ELSE '' END,
		CASE WHEN a.app_key = 'vastora-official/meridian' AND s.app_protocol = 'meridian/entry'
		          AND cutover.state IN ('project','verify') AND cutover.subscription_authority = 'meridian'
		          AND NOT COALESCE(meridian.status = 'ready' AND meridian.runtime_healthy = 1 AND meridian.desired_revision = meridian.applied_revision, 0)
		     THEN CASE WHEN a.id = cutover.legacy_controller_application_id THEN 'controller' ELSE 'worker' END
		     ELSE '' END, s.proxy_protocol, s.protocol
		FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id
		LEFT JOIN three_x_ui_reality_guards g ON g.service_id = s.id
		LEFT JOIN meridian_endpoints meridian ON meridian.service_id = s.id
		LEFT JOIN meridian_cutover cutover ON cutover.id = 1
		WHERE p.ingress_owner = 'application_node' AND p.entry_node_id = ? AND p.kind = 'public_shared_443' AND a.node_id = ?
		AND p.status <> 'stopped' AND s.status <> 'stopped'
		AND (a.runtime='host' OR meridian.id IS NULL OR meridian.vless_enabled=1) ORDER BY p.id`, nodeID, nodeID)
	if err != nil {
		return gateway.NodeListenerState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var route gateway.Layer4Route
		var endpoint, applicationID, applicationNodeID, runtime, role, appKey, cutoverLegacyRuntime, serviceForwarding, serviceProtocol string
		var containerPort, managedReality, runtimeGeneration, actionRequired int
		if err := rows.Scan(&route.ID, &route.Hostname, &endpoint, &applicationID, &applicationNodeID, &runtime, &role, &appKey, &runtimeGeneration, &containerPort, &actionRequired, &managedReality, &route.ProxyProtocol, &cutoverLegacyRuntime, &serviceForwarding, &serviceProtocol); err != nil {
			return gateway.NodeListenerState{}, err
		}
		route.ManagedReality = managedReality != 0
		if applicationNodeID != nodeID {
			return gateway.NodeListenerState{}, errors.New("center: node-direct listener upstream belongs to another Agent")
		}
		route.ApplicationNodeID = applicationNodeID
		if runtime == "host" {
			// Native packages report their own private service endpoint and the
			// signed recipe declares forwarding metadata. Do not infer a proxy
			// component, fixed port or Docker alias from the application's key.
			host, value, err := net.SplitHostPort(endpoint)
			port, portErr := strconv.Atoi(value)
			if err != nil || portErr != nil || port < 1 || port > 65535 || serviceProtocol != "tcp" || !networking.IsPrivateServiceAddress(host) || net.ParseIP(host).IsLoopback() || (serviceForwarding != "" && serviceForwarding != gateway.ProxyProtocolV2) {
				return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("native shared listener requires a declared private TCP service")}
			}
			var owned int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_resources r WHERE r.application_id=? AND r.adoption_state='ready' AND json_extract(r.resources_json,'$.applicationId')=? AND json_extract(r.resources_json,'$.appKey')=? AND json_extract(r.resources_json,'$.runtime') IN ('systemd','native') AND json_extract(r.resources_json,'$.state')='ready' AND json_extract(r.resources_json,'$.manifestSha256')=r.manifest_sha256)`, applicationID, applicationID, appKey).Scan(&owned); err != nil || owned != 1 {
				return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("native shared backend must match its ready installation and confirmed node service address")}
			}
			if restore != nil {
				// Reinstall withdraws the active profile until business acceptance.
				// Match its explicitly approved retained address before migration.
				if applicationID != restore.ApplicationID || appKey != "vastora-official/glivela-entry" || actionRequired != 0 || restore.PreviousBackendAddress == "" || host != restore.PreviousBackendAddress {
					return gateway.NodeListenerState{}, errors.New("center: native recovery must retain its original publication")
				}
				host = restore.BackendAddress
			} else {
				var address string
				if err := tx.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id=?`, nodeID).Scan(&address); err != nil || address != host {
					return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("native shared backend must match its ready installation and confirmed node service address")}
				}
			}
			route.ManagedReality = false
			route.ProxyProtocol = serviceForwarding
			route.Upstreams = []gateway.Upstream{{Address: host, Port: port}}
			routes = append(routes, route)
			continue
		}
		if restore != nil {
			if actionRequired != 0 {
				return gateway.NodeListenerState{}, errors.New("center: resolve pending entry changes before restoring its listener")
			}
			if applicationID != restore.ApplicationID || appKey != glivelaAppKey || !route.ManagedReality {
				return gateway.NodeListenerState{}, errors.New("center: restore other shared-listener applications before restoring this entry")
			}
			cutoverLegacyRuntime = ""
			endpoint = net.JoinHostPort(restore.BackendAddress, strconv.Itoa(glivela.RealityBackendPort))
		}
		if route.ManagedReality && (appKey != glivelaAppKey || cutoverLegacyRuntime != "") {
			alias := dockerruntime.ThreeXUIAlias
			if appKey == glivelaAppKey {
				alias = dockerruntime.LegacyGlivelaAlias
				if cutoverLegacyRuntime == "controller" {
					alias = dockerruntime.ThreeXUIAlias
				} else if cutoverLegacyRuntime == "worker" {
					alias = dockerruntime.LegacyXrayAlias
				}
			} else if role == threeXUIRoleWorker && runtimeGeneration >= 2 {
				alias = dockerruntime.LegacyXrayAlias
			}
			endpoint = net.JoinHostPort(alias, strconv.Itoa(centerThreeXUIRealityPort))
		} else if !route.ManagedReality {
			endpoint = canonicalGatewayServiceEndpoint(appKey, runtime, role, applicationNodeID, nodeID, "", containerPort, endpoint)
		}
		host, portValue, err := net.SplitHostPort(endpoint)
		if err != nil {
			return gateway.NodeListenerState{}, errors.New("center: invalid node-direct listener endpoint")
		}
		port, _ := strconv.Atoi(portValue)
		expectedAlias := dockerruntime.ThreeXUIAlias
		if appKey == glivelaAppKey {
			expectedAlias = dockerruntime.LegacyGlivelaAlias
			if cutoverLegacyRuntime == "controller" {
				expectedAlias = dockerruntime.ThreeXUIAlias
			} else if cutoverLegacyRuntime == "worker" {
				expectedAlias = dockerruntime.LegacyXrayAlias
			}
		} else if role == threeXUIRoleWorker && runtimeGeneration >= 2 {
			expectedAlias = dockerruntime.LegacyXrayAlias
		}
		hostBackend := appKey == glivelaAppKey && cutoverLegacyRuntime == "" && networking.IsPrivateServiceAddress(host) && port == glivela.RealityBackendPort
		if route.ManagedReality && (runtime != "docker" || (!hostBackend && (host != expectedAlias || port != centerThreeXUIRealityPort)) || route.ProxyProtocol != gateway.ProxyProtocolV2) {
			return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: errors.New("managed REALITY listener requires its approved local Xray backend with Proxy Protocol v2")}
		}
		route.Upstreams = []gateway.Upstream{{Address: host, Port: port}}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return gateway.NodeListenerState{}, err
	}
	if len(routes) == 0 {
		if restore != nil {
			return gateway.NodeListenerState{}, errors.New("center: no saved Glivela shared entry to restore")
		}
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
	bindAddress := ""
	if restore != nil {
		bindAddress = restore.BindAddress
	} else {
		_, bindAddress, err = validateNodeDirectPublicIngress(ctx, tx, nodeID)
		if err != nil {
			return gateway.NodeListenerState{}, nodeListenerPrerequisiteError{cause: err}
		}
	}
	useCaddyFallback := false
	if restore != nil {
		var sharedGateway int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM site_gateways membership
			JOIN gateway_components component ON component.gateway_node_id = membership.agent_id
			WHERE membership.agent_id = ? AND component.desired_status = 'running'
		)`, nodeID).Scan(&sharedGateway); err != nil {
			return gateway.NodeListenerState{}, err
		}
		if sharedGateway != 0 {
			return gateway.NodeListenerState{}, errors.New("center: restore the shared gateway before restoring this entry")
		}
		standalonePublicHTTPS, err := s.standaloneDaylightPublicHTTPSFallback(ctx, tx, nodeID, bindAddress, routes)
		if err != nil {
			return gateway.NodeListenerState{}, err
		}
		if standalonePublicHTTPS {
			return gateway.NodeListenerState{}, errors.New("center: restore the shared gateway before restoring this entry")
		}
	} else {
		var fallbackErr error
		useCaddyFallback, fallbackErr = s.nodeListenerNeedsCaddyFallback(ctx, tx, nodeID, bindAddress, routes)
		if fallbackErr != nil {
			return gateway.NodeListenerState{}, fallbackErr
		}
	}
	listener := gateway.SharedHTTPS{Address: bindAddress, Port: 443, RejectUnmatched: !useCaddyFallback, Routes: routes}
	if useCaddyFallback {
		listener.CaddyAddress, listener.CaddyPort = dockerruntime.CaddyAlias, 443
	}
	state := gateway.NodeListenerState{Revision: revision, NodeID: nodeID, Listener: listener}
	if err := validateCenterNodeListenerState(state); err != nil {
		return gateway.NodeListenerState{}, err
	}
	return state.Sorted(), nil
}

// nodeListenerNeedsCaddyFallback returns true only when Caddy explicitly serves
// public HTTPS on the same bindAddress:443 socket as the node-direct listener.
// Site Gateway membership, a merely ready gateway component, private TLS routes,
// or loopback-only Daylight self-camouflage sites never qualify.
func (s *Store) nodeListenerNeedsCaddyFallback(ctx context.Context, tx *sql.Tx, nodeID, bindAddress string, routes []gateway.Layer4Route) (bool, error) {
	hasGatewayHTTPS, err := s.gatewayHasPublicHTTPSFallback(ctx, tx, nodeID, bindAddress)
	if err != nil || hasGatewayHTTPS {
		return hasGatewayHTTPS, err
	}
	return s.standaloneDaylightPublicHTTPSFallback(ctx, tx, nodeID, bindAddress, routes)
}

func (s *Store) gatewayHasPublicHTTPSFallback(ctx context.Context, tx *sql.Tx, nodeID, bindAddress string) (bool, error) {
	var componentReady bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM gateway_components
		WHERE gateway_node_id = ? AND desired_status = 'running' AND status = 'ready'
	)`, nodeID).Scan(&componentReady); err != nil {
		return false, err
	}
	if !componentReady {
		return false, nil
	}
	desired, err := s.desiredGatewayState(ctx, tx, nodeID, 1)
	if err != nil {
		return false, err
	}
	if !gatewayStateHasPublicHTTPSOn(desired, bindAddress, 443) {
		return false, nil
	}
	// Require applied gateway-route evidence rather than waiting for public
	// reachability verification (which traverses this fallback) or enabling
	// fallback before Caddy has applied its public 443 listener.
	var desiredRevision, appliedRevision int64
	var status string
	var desiredJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, desired_json FROM gateway_states WHERE gateway_node_id = ?`, nodeID).Scan(&desiredRevision, &appliedRevision, &status, &desiredJSON); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if appliedRevision < 1 {
		return false, nil
	}
	// When the stored desired revision is already applied, desired_json is the
	// exact gateway payload running in Caddy (covering both system and site routes).
	if appliedRevision == desiredRevision && status == "ready" {
		var appliedState gateway.DesiredState
		if json.Unmarshal(desiredJSON, &appliedState) == nil && appliedState.Revision == appliedRevision {
			return gatewayStateHasPublicHTTPSOn(appliedState, bindAddress, 443), nil
		}
	}
	var appliedPublicRoute bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM routes r
		JOIN services s ON s.id = r.service_id
		JOIN publications p ON p.id = r.publication_id
		JOIN agent_network_profiles n ON n.agent_id = r.gateway_node_id
		WHERE r.gateway_node_id = ?
		  AND r.tls_enabled = 1
		  AND r.applied_revision > 0
		  AND p.kind = ?
		  AND p.ingress_owner = ?
		  AND p.status <> 'stopped'
		  AND s.status <> 'stopped'
		  AND n.public_bind_address = ?
	)`, nodeID, publicationPublic, ingressSiteGateway, bindAddress).Scan(&appliedPublicRoute); err != nil {
		return false, err
	}
	if appliedPublicRoute {
		return true, nil
	}
	// While a follow-up gateway revision is pending, keep an already-active Caddy
	// fallback on this exact socket when system public HTTPS routes still require
	// it, but never promote reject to Caddy from an earlier applied revision alone.
	if gatewayStateHasSystemPublicHTTPSOn(desired, bindAddress, 443) {
		return nodeListenerHasActiveCaddyFallback(ctx, tx, nodeID, bindAddress, 443)
	}
	return false, nil
}

func gatewayStateHasPublicHTTPSOn(state gateway.DesiredState, bindAddress string, port int) bool {
	if !gatewayStateHasPublicListenerOn(state, bindAddress, port) {
		return false
	}
	for _, route := range state.Routes {
		if route.ListenerKind == "public" && route.TLSEnabled {
			return true
		}
	}
	return false
}

func gatewayStateHasSystemPublicHTTPSOn(state gateway.DesiredState, bindAddress string, port int) bool {
	if !gatewayStateHasPublicListenerOn(state, bindAddress, port) {
		return false
	}
	for _, route := range state.Routes {
		if route.System && route.ListenerKind == "public" && route.TLSEnabled {
			return true
		}
	}
	return false
}

func gatewayStateHasPublicListenerOn(state gateway.DesiredState, bindAddress string, port int) bool {
	for _, listener := range state.Listeners {
		if listener.Kind == "public" && listener.Address == bindAddress && listener.HTTPSPort == port {
			return true
		}
	}
	return false
}

func nodeListenerHasActiveCaddyFallback(ctx context.Context, tx *sql.Tx, nodeID, bindAddress string, port int) (bool, error) {
	var appliedRevision int64
	var desiredJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT applied_revision, desired_json FROM node_listener_states WHERE node_id = ?`, nodeID).Scan(&appliedRevision, &desiredJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if appliedRevision < 1 {
		return false, nil
	}
	var current gateway.NodeListenerState
	if json.Unmarshal(desiredJSON, &current) != nil {
		return false, nil
	}
	return len(current.Listener.Routes) != 0 &&
		!current.Listener.RejectUnmatched &&
		current.Listener.Address == bindAddress &&
		current.Listener.Port == port &&
		current.Listener.CaddyAddress == dockerruntime.CaddyAlias &&
		current.Listener.CaddyPort == 443, nil
}

func (s *Store) standaloneDaylightPublicHTTPSFallback(ctx context.Context, tx *sql.Tx, nodeID, bindAddress string, routes []gateway.Layer4Route) (bool, error) {
	type siteCandidate struct {
		applicationID   string
		hostname        string
		migrationFrom   string
		httpsListen     string
		revision        int64
		appliedRevision int64
		state           string
		tlsLoopbackOnly bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.application_id, n.hostname, n.migration_from, n.revision, n.applied_revision, n.state, n.tls_loopback_only, COALESCE(json_extract(d.config_json, '$.https_listen'), '')
		FROM node_sites n
		JOIN applications a ON a.id = n.application_id
		JOIN deployments d ON d.rowid = (
			SELECT latest.rowid FROM deployments latest
			WHERE latest.application_id = a.id
			  AND latest.agent_id = a.node_id
			  AND latest.state = 'succeeded'
			ORDER BY latest.updated_at DESC, latest.rowid DESC LIMIT 1
		)
		WHERE n.agent_id = ?
		  AND a.node_id = ?
		  AND a.app_key = 'vastora-official/daylight-https'
		  AND a.status <> 'stopped'
		  AND d.operation IN ('install', 'upgrade', 'configure')`, nodeID, nodeID)
	if err != nil {
		return false, err
	}
	var candidates []siteCandidate
	for rows.Next() {
		var c siteCandidate
		if err := rows.Scan(&c.applicationID, &c.hostname, &c.migrationFrom, &c.revision, &c.appliedRevision, &c.state, &c.tlsLoopbackOnly, &c.httpsListen); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	realityHosts := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.ManagedReality {
			realityHosts[strings.ToLower(route.Hostname)] = true
		}
	}
	for _, c := range candidates {
		host, port, splitErr := net.SplitHostPort(strings.TrimSpace(c.httpsListen))
		if splitErr != nil || host != bindAddress || port != "443" {
			continue
		}
		nonRealityHost := (c.hostname != "" && !realityHosts[strings.ToLower(c.hostname)]) ||
			(c.migrationFrom != "" && !realityHosts[strings.ToLower(c.migrationFrom)])
		if !nonRealityHost {
			continue
		}
		active, err := nodeSiteServesPublicHTTPS(ctx, tx, nodeID, c.applicationID, c.revision, c.appliedRevision, c.state, c.tlsLoopbackOnly)
		if err != nil || active {
			return active, err
		}
	}
	return false, nil
}

func nodeSiteServesPublicHTTPS(ctx context.Context, tx *sql.Tx, nodeID, applicationID string, revision, appliedRevision int64, state string, tlsLoopbackOnly bool) (bool, error) {
	if state == "pending" && !tlsLoopbackOnly {
		var applyPending bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM node_site_tasks
			WHERE application_id = ?
			  AND agent_id = ?
			  AND revision = ?
			  AND state IN ('pending', 'running')
			  AND json_extract(input_json, '$.action') IN ('apply', 'prepare-migration')
		)`, applicationID, nodeID, revision).Scan(&applyPending); err != nil {
			return false, err
		}
		if applyPending {
			return true, nil
		}
	}
	if state == "rolled_back" {
		// Rollback restores the Agent's previous site snapshot. Only when an
		// earlier succeeded apply/migration revision served public TLS (not
		// loopback-only) did rollback retain a public 443 listener; rolling back
		// an initial TLS revision restores the non-TLS site.
		loopback, found, err := latestNodeSiteTaskLoopback(ctx, tx, nodeID, applicationID, revision, true)
		if err != nil || !found {
			return false, err
		}
		return !loopback, nil
	}
	if appliedRevision <= 0 {
		return false, nil
	}
	if state == "ready" && appliedRevision == revision {
		return !tlsLoopbackOnly, nil
	}
	// Another revision is pending/issuing/action_required while appliedRevision
	// remains active; check the recorded result for appliedRevision if present.
	loopback, found, err := latestNodeSiteTaskLoopback(ctx, tx, nodeID, applicationID, appliedRevision, false)
	if err != nil {
		return false, err
	}
	if found {
		return !loopback, nil
	}
	return !tlsLoopbackOnly, nil
}

func latestNodeSiteTaskLoopback(ctx context.Context, tx *sql.Tx, nodeID, applicationID string, revision int64, strictlyBefore bool) (bool, bool, error) {
	var loopback int
	var err error
	if strictlyBefore {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(result_json, '$.tlsLoopbackOnly'), 0)
			FROM node_site_tasks
			WHERE application_id = ?
			  AND agent_id = ?
			  AND state = 'succeeded'
			  AND json_extract(input_json, '$.action') IN ('apply', 'prepare-migration', 'accept-migration', 'cancel-migration')
			  AND revision < ?
			ORDER BY revision DESC, rowid DESC
			LIMIT 1`, applicationID, nodeID, revision).Scan(&loopback)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(result_json, '$.tlsLoopbackOnly'), 0)
			FROM node_site_tasks
			WHERE application_id = ?
			  AND agent_id = ?
			  AND state = 'succeeded'
			  AND json_extract(input_json, '$.action') IN ('apply', 'prepare-migration', 'accept-migration', 'cancel-migration')
			  AND revision = ?
			ORDER BY revision DESC, rowid DESC
			LIMIT 1`, applicationID, nodeID, revision).Scan(&loopback)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return loopback != 0, true, nil
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

func (s *Store) completeNodeListenerState(ctx context.Context, commit projectionCommit, agentID string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.projectNodeListenerState(ctx, tx, commit, agentID, revision, expectedAttempt, succeeded, taskError)
}

func (s *Store) projectNodeListenerState(ctx context.Context, tx *sql.Tx, commit projectionCommit, agentID string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	var recoveryTask bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_app_preparations WHERE listener_task_id=?)`, nodeListenerTaskID(agentID, revision)).Scan(&recoveryTask); err != nil {
		return err
	}
	if blocked, err := agentReinstallBlocked(ctx, tx, agentID); err != nil {
		return err
	} else if blocked || recoveryTask {
		return errExecutionBlocked
	}
	var err error
	var desired, applied, attempt int64
	var status string
	var desiredJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt, desired_json FROM node_listener_states WHERE node_id = ?`, agentID).Scan(&desired, &applied, &status, &attempt, &desiredJSON); err != nil {
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
			// Applying the listener proves neither DNS nor public reachability.
			// Preserve an outstanding managed DNS failure until the verifier has
			// actually reconciled it; the listener's own status is already ready.
			_, err = tx.ExecContext(ctx, `UPDATE publications SET applied_revision = desired_revision,
				status = CASE WHEN dns_provider = 'cloudflare' AND status = 'failed' AND last_error <> '' THEN status ELSE 'applying' END,
				last_error = CASE WHEN dns_provider = 'cloudflare' AND status = 'failed' AND last_error <> '' THEN last_error ELSE '' END,
				updated_at = ? WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped' AND action_required = 0`, now, agentID)
		}
		if err == nil {
			verificationTargets, err = s.publicationVerificationTargetsForNodeListener(ctx, tx, agentID)
		}
		if err == nil {
			var appliedState gateway.NodeListenerState
			if json.Unmarshal(desiredJSON, &appliedState) == nil && len(appliedState.Listener.Routes) != 0 && !appliedState.Listener.RejectUnmatched {
				gatewayTargets, gatewayErr := s.publicationVerificationTargetsForGateway(ctx, tx, agentID)
				if gatewayErr != nil {
					err = gatewayErr
				} else {
					verificationTargets = append(verificationTargets, gatewayTargets...)
				}
			}
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
	if err := commit(tx); err != nil {
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
	profile, err := networkProfile(ctx, tx, nodeID)
	if err != nil {
		return fmt.Errorf("center: inspect node-listener network profile: %w", err)
	}
	reason := "node-direct listener state differs from Center; queued for reconcile"
	if !healthy {
		reason = "node-direct listener health check failed; queued for reconcile"
	}
	if profile == nil {
		reason = "Node network is recovering"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'degraded', last_error = ?, updated_at = ?
		WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, reason, now.Format(time.RFC3339Nano), nodeID); err != nil {
		return fmt.Errorf("center: mark unhealthy node-direct publications: %w", err)
	}
	if profile == nil {
		// Commit fresh heartbeat/address observations while the normal network
		// confirmation flow restores the prerequisite. Do not queue an
		// unverified listener or roll back the observations needed to repair it.
		return nil
	}
	if err := s.queueNodeListenerState(ctx, tx, nodeID, now); err != nil {
		return fmt.Errorf("center: queue mismatched live node-listener state: %w", err)
	}
	return nil
}

func (s *Store) publicationVerificationTargetsForNodeListener(ctx context.Context, tx *sql.Tx, nodeID string) ([]publicationVerificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, desired_revision FROM publications WHERE ingress_owner = 'application_node' AND entry_node_id = ? AND kind = 'public_shared_443' AND status IN ('applying', 'failed') AND applied_revision = desired_revision AND action_required = 0`, nodeID)
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
