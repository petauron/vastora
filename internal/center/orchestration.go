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

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"golang.org/x/mod/semver"
)

type ApplicationServiceResult struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Address       string `json:"address"`
}

type ApplicationTaskResult struct {
	Services            []ApplicationServiceResult       `json:"services"`
	GeneratedSecrets    map[string]string                `json:"generatedSecrets,omitempty"`
	ApplicationCommand  *RealityCommandResult            `json:"applicationCommand,omitempty"`
	SubscriptionCommand *SubscriptionCommandResult       `json:"subscriptionCommand,omitempty"`
	ClientCommand       *ThreeXUIClientCommandResult     `json:"clientCommand,omitempty"`
	NodeCommand         *ThreeXUINodeCommandResult       `json:"nodeCommand,omitempty"`
	ControllerCommand   *ThreeXUIControllerCommandResult `json:"controllerCommand,omitempty"`
}

func (s *Store) prepareApplication(ctx context.Context, tx *sql.Tx, request DeploymentRequest, manifest catalog.AppManifest, now time.Time) (string, error) {
	var siteID string
	var capabilitiesJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT site_id, capabilities_json FROM agents WHERE id = ?`, request.AgentID).Scan(&siteID, &capabilitiesJSON); err != nil {
		return "", errors.New("center: target node not found")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil || (request.AppKey != komariAppKey && !capabilities.Docker) {
		return "", errors.New("center: target node does not report Docker capability")
	}
	var applicationID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM applications WHERE node_id = ? AND app_key = ?`, request.AgentID, request.AppKey).Scan(&applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		var randomErr error
		applicationID, randomErr = randomToken(18)
		if randomErr != nil {
			return "", randomErr
		}
		image := ""
		if len(manifest.Images) != 0 {
			image = manifest.Images[0].Reference
		}
		runtime := "docker"
		if request.AppKey == komariAppKey {
			runtime = "host"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`, applicationID, manifest.Name.English, request.AgentID, siteID, request.AppKey, image, runtime, request.Role, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return "", fmt.Errorf("center: create application: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("center: read application: %w", err)
	} else if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'pending', site_id = ?, runtime = CASE WHEN app_key = ? THEN 'host' ELSE runtime END, role = CASE WHEN ? = 'install' THEN ? ELSE role END, updated_at = ? WHERE id = ?`, siteID, komariAppKey, request.Operation, request.Role, now.Format(time.RFC3339Nano), applicationID); err != nil {
		return "", fmt.Errorf("center: update application: %w", err)
	}
	if request.AppKey == threeXUIAppKey && request.Operation == "install" && request.Role == threeXUIRoleMaster {
		if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_nodes WHERE worker_application_id = ?`, applicationID); err != nil {
			return "", fmt.Errorf("center: reset 3x-ui topology role: %w", err)
		}
	}
	if request.AppKey == threeXUIAppKey && request.Operation == "install" && request.Role == threeXUIRoleWorker {
		var masterApplicationID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM applications WHERE site_id = ? AND app_key = ? AND role = 'master' AND status = 'running'`, siteID, threeXUIAppKey).Scan(&masterApplicationID); err != nil {
			return "", errors.New("center: this Site has no running 3x-ui controller")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id, master_application_id, status, last_error, created_at, updated_at)
			VALUES(?, ?, 'pending', '', ?, ?)
			ON CONFLICT(worker_application_id) DO UPDATE SET master_application_id = excluded.master_application_id, remote_node_id = NULL, status = 'pending', last_error = '', updated_at = excluded.updated_at`,
			applicationID, masterApplicationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return "", fmt.Errorf("center: reserve 3x-ui VLESS node: %w", err)
		}
	}
	return applicationID, nil
}

func (s *Store) completeApplication(ctx context.Context, tx *sql.Tx, deploymentID, applicationID, operation string, executedRuntimeGeneration int, result ApplicationTaskResult, now time.Time, cleanups *[]publicationCleanup) error {
	if operation == "uninstall" {
		values, err := s.applicationPublicationCleanups(ctx, tx, applicationID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'stopped', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', updated_at = ? WHERE application_id = ?`, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
			cleanup_pending = CASE WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
			cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ?
			WHERE service_id IN (SELECT id FROM services WHERE application_id = ?) AND status <> 'stopped'`, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
		if err := s.queueAffectedGateways(ctx, tx, applicationID, now); err != nil {
			return err
		}
		*cleanups = append(*cleanups, values...)
		return nil
	}
	var siteID, role string
	if err := tx.QueryRowContext(ctx, `SELECT site_id, role FROM applications WHERE id = ?`, applicationID).Scan(&siteID, &role); err != nil {
		return fmt.Errorf("center: read application site: %w", err)
	}
	var manifestJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id = ?`, deploymentID).Scan(&manifestJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'running', runtime_generation = ?, runtime = CASE WHEN app_key = ? THEN 'host' ELSE runtime END, updated_at = ? WHERE id = ?`, executedRuntimeGeneration, komariAppKey, now.Format(time.RFC3339Nano), applicationID); err != nil {
		return err
	}
	var manifest catalog.AppManifest
	if json.Unmarshal(manifestJSON, &manifest) != nil {
		return errors.New("center: stored deployment manifest is invalid")
	}
	managementServices := map[string]bool{}
	for _, service := range manifest.Services {
		managementServices[service.Name] = service.Management
	}
	reportedServices := make(map[string]bool, len(result.Services))
	for _, reported := range result.Services {
		if role == threeXUIRoleWorker {
			continue
		}
		reportedServices[reported.Name] = true
		if net.ParseIP(reported.Address) == nil || reported.HostPort < 1 || reported.HostPort > 65535 || reported.ContainerPort < 1 || reported.ContainerPort > 65535 {
			return errors.New("center: Agent reported an invalid service endpoint")
		}
		endpoint := net.JoinHostPort(reported.Address, strconv.Itoa(reported.HostPort))
		var serviceID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE application_id = ? AND name = ?`, applicationID, reported.Name).Scan(&serviceID)
		if errors.Is(err, sql.ErrNoRows) {
			serviceID, err = randomToken(18)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, status, created_at, updated_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'catalog', '', ?, 'ready', ?, ?)`, serviceID, applicationID, siteID, reported.Name, reported.Protocol, reported.ContainerPort, reported.HostPort, endpoint, managementServices[reported.Name], now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
				return fmt.Errorf("center: create service: %w", err)
			}
		} else if err != nil {
			return err
		} else if _, err := tx.ExecContext(ctx, `UPDATE services SET protocol = ?, container_port = ?, host_port = ?, endpoint = ?, source = 'catalog', app_protocol = '', management = ?, status = 'ready', last_error = '', updated_at = ? WHERE id = ?`, reported.Protocol, reported.ContainerPort, reported.HostPort, endpoint, managementServices[reported.Name], now.Format(time.RFC3339Nano), serviceID); err != nil {
			return fmt.Errorf("center: update service: %w", err)
		}
	}
	existingRows, err := tx.QueryContext(ctx, `SELECT id, name FROM services WHERE application_id = ? AND source = 'catalog'`, applicationID)
	if err != nil {
		return err
	}
	type existingService struct{ id, name string }
	existing := []existingService{}
	for existingRows.Next() {
		var service existingService
		if err := existingRows.Scan(&service.id, &service.name); err != nil {
			existingRows.Close()
			return err
		}
		existing = append(existing, service)
	}
	if err := existingRows.Err(); err != nil {
		existingRows.Close()
		return err
	}
	if err := existingRows.Close(); err != nil {
		return err
	}
	for _, service := range existing {
		if reportedServices[service.name] {
			continue
		}
		values, err := s.servicePublicationCleanups(ctx, tx, service.id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), service.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
			cleanup_pending = CASE WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
			cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE service_id = ? AND status <> 'stopped'`, now.Format(time.RFC3339Nano), service.id); err != nil {
			return err
		}
		*cleanups = append(*cleanups, values...)
	}
	return s.reconcileApplicationPublications(ctx, tx, applicationID, now)
}

func (s *Store) queueAffectedGateways(ctx context.Context, tx *sql.Tx, applicationID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.gateway_node_id FROM routes r JOIN services s ON s.id = r.service_id WHERE s.application_id = ?
		UNION SELECT DISTINCT p.gateway_node_id FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.application_id = ? AND p.kind = 'public_shared_443' AND p.gateway_node_id IS NOT NULL`, applicationID, applicationID)
	if err != nil {
		return err
	}
	var gatewayIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		gatewayIDs = append(gatewayIDs, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE service_id IN (SELECT id FROM services WHERE application_id = ?)`, applicationID); err != nil {
		return err
	}
	for _, gatewayID := range gatewayIDs {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueGatewayState(ctx context.Context, tx *sql.Tx, gatewayID string, now time.Time) error {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, gatewayID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	revision := current + 1
	state, err := s.desiredGatewayState(ctx, tx, gatewayID, revision)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_states(gateway_node_id, desired_revision, applied_revision, desired_json, status, updated_at)
		VALUES(?, ?, 0, ?, 'pending', ?)
		ON CONFLICT(gateway_node_id) DO UPDATE SET desired_revision = excluded.desired_revision, desired_json = excluded.desired_json, status = 'pending', lease_expires_at = '', last_error = '', updated_at = excluded.updated_at`, gatewayID, revision, payload, now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: queue gateway state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE routes SET desired_revision = ?, status = 'pending', updated_at = ? WHERE gateway_node_id = ?`, revision, now.Format(time.RFC3339Nano), gatewayID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, gatewayRouteTaskID(gatewayID, revision), gatewayID, "gateway.routes.apply", revision, "queued", "full gateway desired state queued"); err != nil {
		return err
	}
	return nil
}

func (s *Store) desiredGatewayState(ctx context.Context, tx *sql.Tx, gatewayID string, revision int64) (gateway.DesiredState, error) {
	state := gateway.DesiredState{Revision: revision, Listeners: []gateway.Listener{}, Routes: []gateway.Route{}}
	listeners := map[string]gateway.Listener{}
	if err := s.appendSystemGatewayRoutes(ctx, tx, gatewayID, &state, listeners); err != nil {
		return gateway.DesiredState{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.hostname, r.protocol, r.upstreams_json, r.tls_enabled, p.kind,
		n.lan_address, n.headscale_address, n.public_address, n.public_bind_address
		FROM routes r JOIN services s ON s.id = r.service_id JOIN publications p ON p.id = r.publication_id
		JOIN agent_network_profiles n ON n.agent_id = r.gateway_node_id
		WHERE r.gateway_node_id = ? AND s.status <> 'stopped' AND p.status <> 'stopped'
		ORDER BY r.id`, gatewayID)
	if err != nil {
		return gateway.DesiredState{}, err
	}
	for rows.Next() {
		var route gateway.Route
		var encoded []byte
		var tlsEnabled int
		var publicationKind, lanAddress, headscaleAddress, publicAddress, publicBindAddress string
		if err := rows.Scan(&route.ID, &route.Hostname, &route.Protocol, &encoded, &tlsEnabled, &publicationKind, &lanAddress, &headscaleAddress, &publicAddress, &publicBindAddress); err != nil {
			return gateway.DesiredState{}, err
		}
		route.TLSEnabled = tlsEnabled == 1
		address := lanAddress
		route.ListenerKind = "lan"
		if publicationKind == publicationHeadscale {
			address, route.ListenerKind = headscaleAddress, "headscale"
		}
		if publicationKind == publicationPublic {
			address, route.ListenerKind = publicBindAddress, "public"
		}
		if publicationKind == publicationCloudflare {
			address, route.ListenerKind, route.TLSEnabled = "127.0.0.1", "system", false
		}
		listeners[route.ListenerKind] = gateway.Listener{Kind: route.ListenerKind, Address: address, HTTPPort: 80, HTTPSPort: 443}
		var upstreams []string
		if json.Unmarshal(encoded, &upstreams) != nil {
			return gateway.DesiredState{}, errors.New("center: invalid stored route upstreams")
		}
		for _, value := range upstreams {
			host, portValue, err := net.SplitHostPort(value)
			if err != nil {
				return gateway.DesiredState{}, errors.New("center: invalid stored route endpoint")
			}
			port, _ := strconv.Atoi(portValue)
			route.Upstreams = append(route.Upstreams, gateway.Upstream{Address: host, Port: port})
		}
		state.Routes = append(state.Routes, route)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return gateway.DesiredState{}, err
	}
	if err := rows.Close(); err != nil {
		return gateway.DesiredState{}, err
	}
	sharedRows, err := tx.QueryContext(ctx, `SELECT p.id, p.sni_hostname, s.endpoint, n.public_bind_address,
		application.node_id, application.runtime, application.app_key, s.container_port,
		CASE WHEN application.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality' AND guard.status = 'ready' THEN 'v2' ELSE '' END
		FROM publications p JOIN services s ON s.id = p.service_id
		JOIN applications application ON application.id = s.application_id
		JOIN agent_network_profiles n ON n.agent_id = p.gateway_node_id
		LEFT JOIN three_x_ui_reality_guards guard ON guard.service_id = s.id
		WHERE p.gateway_node_id = ? AND p.kind = 'public_shared_443'
		AND p.status <> 'stopped' AND s.status <> 'stopped' ORDER BY p.id`, gatewayID)
	if err != nil {
		return gateway.DesiredState{}, err
	}
	var shared *gateway.SharedHTTPS
	for sharedRows.Next() {
		var route gateway.Layer4Route
		var endpoint, publicAddress, applicationNodeID, runtime, appKey string
		var containerPort int
		if err := sharedRows.Scan(&route.ID, &route.Hostname, &endpoint, &publicAddress, &applicationNodeID, &runtime, &appKey, &containerPort, &route.ProxyProtocol); err != nil {
			sharedRows.Close()
			return gateway.DesiredState{}, err
		}
		endpoint = canonicalGatewayServiceEndpoint(appKey, runtime, applicationNodeID, gatewayID, containerPort, endpoint)
		host, portValue, err := net.SplitHostPort(endpoint)
		if err != nil {
			sharedRows.Close()
			return gateway.DesiredState{}, errors.New("center: invalid stored shared 443 endpoint")
		}
		port, _ := strconv.Atoi(portValue)
		route.Upstreams = []gateway.Upstream{{Address: host, Port: port}}
		if shared == nil {
			shared = &gateway.SharedHTTPS{Address: publicAddress, Port: 443, CaddyAddress: dockerruntime.CaddyAlias, CaddyPort: 443}
			listeners["public"] = gateway.Listener{Kind: "public", Address: publicAddress, HTTPPort: 80, HTTPSPort: 443}
		} else if shared.Address != publicAddress {
			sharedRows.Close()
			return gateway.DesiredState{}, errors.New("center: shared 443 publications disagree on the public address")
		}
		shared.Routes = append(shared.Routes, route)
	}
	if err := sharedRows.Close(); err != nil {
		return gateway.DesiredState{}, err
	}
	state.SharedHTTPS = shared
	for _, listener := range listeners {
		state.Listeners = append(state.Listeners, listener)
	}
	if err := state.Validate(); err != nil {
		return gateway.DesiredState{}, err
	}
	return state.Sorted(), nil
}

func (s *Store) CompleteGatewayState(ctx context.Context, agentID, credential string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
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
	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM gateway_states WHERE gateway_node_id = ?`, agentID).Scan(&desired, &applied, &currentStatus, &attempt); err != nil {
		return errors.New("center: gateway desired state not found")
	}
	if revision <= applied {
		return nil
	}
	if revision != desired {
		return errors.New("center: stale gateway result")
	}
	if currentStatus != "applying" {
		return errors.New("center: gateway task is not active")
	}
	if expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale gateway result")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if !succeeded {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_states SET status = 'failed', lease_expires_at = '', last_error = ?, updated_at = ? WHERE gateway_node_id = ?`, taskError, now, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET status = 'failed', last_error = ?, updated_at = ? WHERE gateway_node_id = ? AND desired_revision = ?`, taskError, now, agentID, revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ?
			WHERE id IN (SELECT publication_id FROM routes WHERE gateway_node_id = ? AND desired_revision = ?)`, taskError, now, agentID, revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ?
			WHERE gateway_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, taskError, now, agentID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_states SET applied_revision = ?, status = 'ready', lease_expires_at = '', last_error = '', updated_at = ? WHERE gateway_node_id = ?`, revision, now, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET applied_revision = ?, status = 'ready', last_error = '', updated_at = ? WHERE gateway_node_id = ? AND desired_revision = ?`, revision, now, agentID, revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET applied_revision = desired_revision,
			status = CASE
				WHEN kind = 'public_direct' AND status = 'failed' AND dns_provider <> 'manual' THEN 'failed'
				WHEN kind IN ('public_direct', 'cloudflare_tunnel') THEN 'applying'
				ELSE 'ready'
			END,
			last_error = CASE WHEN status = 'failed' AND dns_provider <> 'manual' THEN last_error ELSE '' END,
			updated_at = ?
			WHERE id IN (SELECT publication_id FROM routes WHERE gateway_node_id = ? AND applied_revision = ?)`, now, agentID, revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET applied_revision = desired_revision,
			status = CASE WHEN status = 'failed' AND dns_provider <> 'manual' THEN 'failed' ELSE 'applying' END,
			last_error = CASE WHEN status = 'failed' AND dns_provider <> 'manual' THEN last_error ELSE '' END,
			updated_at = ?
			WHERE gateway_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, now, agentID); err != nil {
			return err
		}
	}
	event := "succeeded"
	if !succeeded {
		event = "failed"
	}
	if err := s.recordTaskEvent(ctx, tx, gatewayRouteTaskID(agentID, revision), agentID, "gateway.routes.apply", revision, event, taskError); err != nil {
		return err
	}
	verificationTargets := []publicationVerificationTarget{}
	if succeeded {
		verificationTargets, err = s.publicationVerificationTargetsForGateway(ctx, tx, agentID)
		if err != nil {
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

func gatewayTaskRevision(taskID string) (int64, bool) {
	marker := strings.LastIndex(taskID, "-r")
	if !strings.HasPrefix(taskID, "gateway-route-") || marker < len("gateway-route-") {
		return 0, false
	}
	revision, err := strconv.ParseInt(taskID[marker+2:], 10, 64)
	return revision, err == nil && revision > 0
}

func gatewayRouteTaskID(agentID string, revision int64) string {
	return fmt.Sprintf("gateway-route-%s-r%d", agentID, revision)
}

func (s *Store) ListApplications(ctx context.Context) ([]ApplicationView, error) {
	apps, err := s.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	availableVersions := make(map[string]string, len(apps))
	for _, app := range apps {
		availableVersions[app.Key] = app.App.Version
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.name, a.node_id, a.site_id, a.app_key, a.image, a.status, a.runtime, a.role,
		COALESCE(n.master_application_id, ''), COALESCE(n.status, ''), COALESCE(n.last_error, ''),
		COALESCE(b.state, ''), COALESCE(b.updated_at, ''),
		COALESCE(CASE WHEN latest.operation IN ('install', 'upgrade', 'configure') THEN latest.app_version ELSE '' END, ''),
		a.created_at, a.updated_at
		FROM applications a
		LEFT JOIN three_x_ui_nodes n ON n.worker_application_id = a.id
		LEFT JOIN three_x_ui_backups b ON b.application_id = a.id
		LEFT JOIN deployments latest ON latest.rowid = (
			SELECT candidate.rowid FROM deployments candidate
			WHERE candidate.agent_id = a.node_id AND candidate.app_key = a.app_key AND candidate.state = 'succeeded'
			ORDER BY candidate.created_at DESC, candidate.rowid DESC LIMIT 1
		)
		ORDER BY a.name, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ApplicationView, 0)
	for rows.Next() {
		var value ApplicationView
		var created, updated string
		var restorePointAt string
		if err := rows.Scan(&value.ID, &value.Name, &value.NodeID, &value.SiteID, &value.AppKey, &value.Image, &value.Status, &value.Runtime, &value.Role, &value.ControllerID, &value.NodeSyncStatus, &value.NodeSyncError, &value.RestorePointState, &restorePointAt, &value.InstalledVersion, &created, &updated); err != nil {
			return nil, err
		}
		if restorePointAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, restorePointAt)
			if parseErr != nil {
				return nil, errors.New("center: invalid 3x-ui restore point timestamp")
			}
			value.RestorePointAt = &parsed
		}
		value.AvailableVersion = availableVersions[value.AppKey]
		value.UpdateAvailable = value.InstalledVersion != "" && semver.Compare(canonicalAppVersion(value.AvailableVersion), canonicalAppVersion(value.InstalledVersion)) > 0
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ListServices(ctx context.Context) ([]ServiceView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.application_id, s.site_id, s.name, s.display_name, s.region_code, s.protocol, s.container_port, s.host_port, s.endpoint, s.source, s.app_protocol, s.management, s.observed_listen, s.status, s.last_error, s.created_at, s.updated_at, a.last_seen_at,
		COALESCE(guard.status, ''), COALESCE(guard.target_host, ''), COALESCE(guard.target_ip, ''), COALESCE(guard.server_name, ''), COALESCE(guard.target_asn, 0), COALESCE(guard.last_error, '')
		FROM services s JOIN applications app ON app.id = s.application_id JOIN agents a ON a.id = app.node_id
		LEFT JOIN three_x_ui_reality_guards guard ON guard.service_id = s.id ORDER BY s.name, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ServiceView, 0)
	for rows.Next() {
		var value ServiceView
		var created, updated, lastSeen string
		var guardTargetHost, guardTargetIP, guardServerName, guardError string
		var guardTargetASN int64
		var management int
		if err := rows.Scan(&value.ID, &value.ApplicationID, &value.SiteID, &value.Name, &value.DisplayName, &value.RegionCode, &value.Protocol, &value.ContainerPort, &value.HostPort, &value.Endpoint, &value.Source, &value.AppProtocol, &management, &value.ObservedListen, &value.Status, &value.LastError, &created, &updated, &lastSeen, &value.GuardStatus, &guardTargetHost, &guardTargetIP, &guardServerName, &guardTargetASN, &guardError); err != nil {
			return nil, err
		}
		if value.GuardStatus != "" {
			value.GuardSummary = fmt.Sprintf("%s -> %s:443; SNI %s; ASN %d", guardTargetHost, guardTargetIP, guardServerName, guardTargetASN)
			if value.GuardStatus == "action_required" {
				value.ActionRequired = guardError
			}
		}
		value.Management = management == 1
		lastSeenAt, err := time.Parse(time.RFC3339Nano, lastSeen)
		if err != nil {
			return nil, errors.New("center: invalid worker heartbeat timestamp")
		}
		if lastSeenAt.Before(s.now().UTC().Add(-45*time.Second)) && (value.Status == "running" || value.Status == "publishing" || value.Status == "ready") {
			value.Status = "degraded"
			value.LastError = "worker offline; existing gateway route retained"
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ListRoutes(ctx context.Context) ([]RouteView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, desired_revision, applied_revision, last_error, created_at, updated_at FROM routes ORDER BY hostname, gateway_node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RouteView, 0)
	for rows.Next() {
		var value RouteView
		var upstreams []byte
		var tls int
		var created, updated string
		if err := rows.Scan(&value.ID, &value.PublicationID, &value.SiteID, &value.ServiceID, &value.GatewayNodeID, &value.Hostname, &value.Protocol, &upstreams, &tls, &value.Status, &value.DesiredRevision, &value.AppliedRevision, &value.LastError, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(upstreams, &value.Upstreams)
		value.TLSEnabled = tls == 1
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, value)
	}
	return result, rows.Err()
}
