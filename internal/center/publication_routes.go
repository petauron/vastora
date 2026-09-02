package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
)

func (s *Store) upsertPublicationRoute(ctx context.Context, tx *sql.Tx, publicationID, siteID, serviceID, gatewayID, hostname, protocol, endpoint string, tlsEnabled bool, now time.Time) error {
	var err error
	endpoint, err = s.gatewayServiceEndpoint(ctx, tx, serviceID, gatewayID, endpoint)
	if err != nil {
		return err
	}
	upstreams, _ := json.Marshal([]string{endpoint})
	var routeID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM routes WHERE publication_id = ? AND gateway_node_id = ?`, publicationID, gatewayID).Scan(&routeID)
	if errors.Is(err, sql.ErrNoRows) {
		routeID, err = randomToken(18)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, routeID, publicationID, siteID, serviceID, gatewayID, hostname, protocol, upstreams, tlsEnabled, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE routes SET hostname = ?, protocol = ?, upstreams_json = ?, tls_enabled = ?, status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, hostname, protocol, upstreams, tlsEnabled, now.Format(time.RFC3339Nano), routeID)
	}
	if err != nil {
		return fmt.Errorf("center: save publication route: %w", err)
	}
	return nil
}

func (s *Store) gatewayServiceEndpoint(ctx context.Context, tx *sql.Tx, serviceID, gatewayID, endpoint string) (string, error) {
	var appKey, runtime, applicationNodeID string
	var containerPort int
	if err := tx.QueryRowContext(ctx, `SELECT a.app_key, a.runtime, a.node_id, s.container_port
		FROM services s JOIN applications a ON a.id = s.application_id WHERE s.id = ?`, serviceID).Scan(&appKey, &runtime, &applicationNodeID, &containerPort); err != nil {
		return "", fmt.Errorf("center: read publication service runtime: %w", err)
	}
	return canonicalGatewayServiceEndpoint(appKey, runtime, applicationNodeID, gatewayID, containerPort, endpoint), nil
}

func canonicalGatewayServiceEndpoint(appKey, runtime, applicationNodeID, gatewayID string, containerPort int, endpoint string) string {
	if appKey == threeXUIAppKey && runtime == "docker" && applicationNodeID == gatewayID && containerPort > 0 && containerPort <= 65535 {
		return net.JoinHostPort(dockerruntime.ThreeXUIAlias, strconv.Itoa(containerPort))
	}
	return endpoint
}

func (s *Store) discardEmptyRetiredSharedPublicationMarker(ctx context.Context) error {
	var markerExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'retired_shared_publication_gateways'`).Scan(&markerExists); err != nil || markerExists == 0 {
		return err
	}
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM retired_shared_publication_gateways`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DROP TABLE retired_shared_publication_gateways`)
	return err
}

// reconcileRetiredSharedPublicationGateways completes migration 46 after the
// Center starts serving. The SQL migration removes obsolete path routes, while
// this transaction publishes the resulting host-only desired state to every
// affected Gateway before dropping the one-shot marker table.
func (s *Store) reconcileRetiredSharedPublicationGateways(ctx context.Context) error {
	var markerExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'retired_shared_publication_gateways'`).Scan(&markerExists); err != nil {
		return err
	}
	if markerExists == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT gateway_node_id FROM retired_shared_publication_gateways ORDER BY gateway_node_id`)
	if err != nil {
		return err
	}
	gatewayIDs := []string{}
	for rows.Next() {
		var gatewayID string
		if err := rows.Scan(&gatewayID); err != nil {
			rows.Close()
			return err
		}
		gatewayIDs = append(gatewayIDs, gatewayID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC()
	for _, gatewayID := range gatewayIDs {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE retired_shared_publication_gateways`); err != nil {
		return err
	}
	return tx.Commit()
}

// reconcileDockerGatewayEndpoints upgrades persisted same-node routes to the
// Docker bridge address that the managed gateway can actually reach. It only
// queues gateways whose canonical upstream changed, so later Center restarts
// are level-triggered no-ops.
func (s *Store) reconcileDockerGatewayEndpoints(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type routeEndpoint struct {
		routeID, publicationID, gatewayID, appKey, runtime, applicationNodeID, endpoint string
		containerPort                                                                   int
		upstreams                                                                       []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.publication_id, r.gateway_node_id, r.upstreams_json,
		a.app_key, a.runtime, a.node_id, s.container_port, s.endpoint
		FROM routes r
		JOIN publications p ON p.id = r.publication_id
		JOIN services s ON s.id = r.service_id
		JOIN applications a ON a.id = s.application_id
		WHERE p.status <> 'stopped' AND s.status <> 'stopped'`)
	if err != nil {
		return fmt.Errorf("center: inspect Docker gateway endpoints: %w", err)
	}
	values := []routeEndpoint{}
	for rows.Next() {
		var value routeEndpoint
		if err := rows.Scan(&value.routeID, &value.publicationID, &value.gatewayID, &value.upstreams, &value.appKey, &value.runtime, &value.applicationNodeID, &value.containerPort, &value.endpoint); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC()
	gateways := map[string]bool{}
	for _, value := range values {
		canonical := canonicalGatewayServiceEndpoint(value.appKey, value.runtime, value.applicationNodeID, value.gatewayID, value.containerPort, value.endpoint)
		var current []string
		if json.Unmarshal(value.upstreams, &current) == nil && len(current) == 1 && current[0] == canonical {
			continue
		}
		encoded, _ := json.Marshal([]string{canonical})
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET upstreams_json = ?, status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, encoded, now.Format(time.RFC3339Nano), value.routeID); err != nil {
			return fmt.Errorf("center: reconcile Docker gateway endpoint: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'pending', last_error = '', updated_at = ? WHERE id = ? AND status <> 'stopped'`, now.Format(time.RFC3339Nano), value.publicationID); err != nil {
			return err
		}
		gateways[value.gatewayID] = true
	}
	for gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) reconcileApplicationPublications(ctx context.Context, tx *sql.Tx, applicationID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.kind, p.ingress_owner, p.entry_node_id, p.hostname, p.tls_enabled, s.id, s.site_id, s.protocol, s.endpoint
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.application_id = ? AND p.status <> 'stopped' AND s.status <> 'stopped'`, applicationID)
	if err != nil {
		return err
	}
	type item struct {
		publicationID, kind, ingressOwner, gatewayID, hostname, serviceID, siteID, protocol, endpoint string
		tls                                                                                           bool
	}
	items := []item{}
	for rows.Next() {
		var value item
		var gatewayID sql.NullString
		var tls int
		if err := rows.Scan(&value.publicationID, &value.kind, &value.ingressOwner, &gatewayID, &value.hostname, &tls, &value.serviceID, &value.siteID, &value.protocol, &value.endpoint); err != nil {
			rows.Close()
			return err
		}
		value.gatewayID, value.tls = gatewayID.String, tls == 1
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	gateways, nodeListeners, tunnels := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, value := range items {
		if isGatewayPublication(value.ingressOwner) {
			if err := s.upsertPublicationRoute(ctx, tx, value.publicationID, value.siteID, value.serviceID, value.gatewayID, value.hostname, value.protocol, value.endpoint, value.tls, now); err != nil {
				return err
			}
			gateways[value.gatewayID] = true
		}
		if value.ingressOwner == ingressApplicationNode && value.kind == publicationShared443 {
			nodeListeners[value.gatewayID] = true
		}
		if value.ingressOwner == ingressTunnelConnector {
			tunnels[value.gatewayID] = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'pending', desired_revision = desired_revision + 1, last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), value.publicationID); err != nil {
			return err
		}
	}
	for id := range gateways {
		if err := s.queueGatewayState(ctx, tx, id, now); err != nil {
			return err
		}
	}
	for id := range nodeListeners {
		if err := s.queueNodeListenerState(ctx, tx, id, now); err != nil {
			return err
		}
	}
	for id := range tunnels {
		if err := s.queueTunnelState(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return s.retireStoppedMigratedTunnelConnectors(ctx, tx, applicationID, now)
}

func validateGatewayForPublication(ctx context.Context, tx *sql.Tx, siteID, gatewayID, kind string) error {
	var capabilitiesJSON, enabledJSON []byte
	var direct int
	var gatewaySite string
	if err := tx.QueryRowContext(ctx, `SELECT a.site_id, a.capabilities_json, p.enabled_kinds_json, p.direct_public
		FROM agents a
		JOIN agent_network_profiles p ON p.agent_id = a.id
		JOIN site_gateways sg ON sg.agent_id = a.id AND sg.site_id = a.site_id
		WHERE a.id = ?`, gatewayID).Scan(&gatewaySite, &capabilitiesJSON, &enabledJSON, &direct); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: entry node must be selected as a site Gateway with a confirmed network profile")
	} else if err != nil {
		return err
	}
	if gatewaySite != siteID {
		return errors.New("center: entry node must belong to the service site")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.Gateway {
		return errors.New("center: entry node does not report Gateway capability")
	}
	var enabled []string
	if json.Unmarshal(enabledJSON, &enabled) != nil {
		return errors.New("center: entry node has an invalid network profile")
	}
	required := networking.KindLAN
	if kind == publicationHeadscale {
		required = networking.KindHeadscale
	}
	if kind == publicationPublic || kind == publicationShared443 {
		required = networking.KindPublic
		if direct != 1 {
			return errors.New("center: entry node is not approved for direct public ingress")
		}
	}
	for _, value := range enabled {
		if value == required {
			return nil
		}
	}
	return fmt.Errorf("center: entry node does not have %s networking enabled", required)
}

func validateTunnelNode(ctx context.Context, tx *sql.Tx, siteID, nodeID string) error {
	var capabilitiesJSON []byte
	var nodeSite string
	if err := tx.QueryRowContext(ctx, `SELECT site_id, capabilities_json FROM agents WHERE id = ?`, nodeID).Scan(&nodeSite, &capabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: tunnel node not found")
	} else if err != nil {
		return err
	}
	var capabilities NodeCapabilities
	if nodeSite != siteID || json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.Tunnel {
		return errors.New("center: selected node does not have Tunnel capability in this site")
	}
	return nil
}

func validatePublicationOrigin(ctx context.Context, tx *sql.Tx, applicationNodeID, gatewayNodeID, endpoint string) error {
	if applicationNodeID == gatewayNodeID {
		return nil
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return errors.New("center: stored service endpoint is invalid")
	}
	originIP := net.ParseIP(host)
	if originIP == nil || originIP.IsLoopback() || originIP.IsUnspecified() {
		return errors.New("center: cross-node entry requires a routable private service address")
	}
	var serviceAddress, lanAddress, headscaleAddress string
	var originEnabledJSON, gatewayEnabledJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT service_address, lan_address, headscale_address, enabled_kinds_json FROM agent_network_profiles WHERE agent_id = ?`, applicationNodeID).Scan(&serviceAddress, &lanAddress, &headscaleAddress, &originEnabledJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: application node needs a confirmed network profile")
	} else if err != nil {
		return err
	}
	serviceIP := net.ParseIP(serviceAddress)
	if serviceIP == nil || !originIP.Equal(serviceIP) {
		return errors.New("center: service endpoint no longer matches the application node network profile")
	}
	if err := tx.QueryRowContext(ctx, `SELECT enabled_kinds_json FROM agent_network_profiles WHERE agent_id = ?`, gatewayNodeID).Scan(&gatewayEnabledJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: cross-node entry requires a confirmed gateway network profile")
	} else if err != nil {
		return err
	}
	var originEnabled, gatewayEnabled []string
	if json.Unmarshal(originEnabledJSON, &originEnabled) != nil || json.Unmarshal(gatewayEnabledJSON, &gatewayEnabled) != nil {
		return errors.New("center: node has an invalid network profile")
	}
	originKinds := map[string]bool{}
	for _, kind := range originEnabled {
		if kind == networking.KindLAN && net.ParseIP(lanAddress) != nil && originIP.Equal(net.ParseIP(lanAddress)) {
			originKinds[kind] = true
		}
		if kind == networking.KindHeadscale && net.ParseIP(headscaleAddress) != nil && originIP.Equal(net.ParseIP(headscaleAddress)) {
			originKinds[kind] = true
		}
	}
	for _, kind := range gatewayEnabled {
		if originKinds[kind] {
			return nil
		}
	}
	return errors.New("center: entry node cannot reach the application private service network")
}

func validateNodeDirectPublicIngress(ctx context.Context, tx *sql.Tx, nodeID string) (string, string, error) {
	var publicAddress, publicBindAddress, publicMode, enabledJSON string
	var direct int
	if err := tx.QueryRowContext(ctx, `SELECT public_address, public_bind_address, public_mode, CAST(enabled_kinds_json AS TEXT), direct_public FROM agent_network_profiles WHERE agent_id = ?`, nodeID).Scan(&publicAddress, &publicBindAddress, &publicMode, &enabledJSON, &direct); errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("center: application node needs a confirmed public network profile")
	} else if err != nil {
		return "", "", err
	}
	var enabled []string
	publicIP := net.ParseIP(publicAddress)
	bindIP := net.ParseIP(publicBindAddress)
	if json.Unmarshal([]byte(enabledJSON), &enabled) != nil || direct != 1 || publicIP == nil || bindIP == nil || (publicMode != networking.PublicModeDirect && publicMode != networking.PublicModeNAT) {
		return "", "", errors.New("center: application node is not approved for node-direct public ingress")
	}
	for _, kind := range enabled {
		if kind == networking.KindPublic {
			return publicIP.String(), bindIP.String(), nil
		}
	}
	return "", "", errors.New("center: application node does not have public networking enabled")
}

func gatewayHasDirectRaw443(ctx context.Context, tx *sql.Tx, gatewayID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.endpoint FROM publications p JOIN services s ON s.id = p.service_id
		WHERE p.ingress_owner = 'application_node' AND p.entry_node_id = ? AND p.kind = 'public_direct' AND p.status <> 'stopped' AND s.protocol IN ('tcp', 'udp')`, gatewayID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return false, err
		}
		_, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			return false, errors.New("center: stored direct public endpoint is invalid")
		}
		if port == "443" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func validPublicationKind(kind string) bool {
	return kind == publicationLAN || kind == publicationHeadscale || kind == publicationPublic || kind == publicationShared443 || kind == publicationCloudflare
}

func validPublicationDNS(kind, provider string) bool {
	switch kind {
	case publicationLAN:
		return provider == "manual"
	case publicationHeadscale:
		return provider == "headscale" || provider == "manual"
	case publicationPublic, publicationShared443:
		return provider == "cloudflare" || provider == "manual"
	case publicationCloudflare:
		return provider == "cloudflare"
	default:
		return false
	}
}

func isGatewayPublication(ingressOwner string) bool {
	return ingressOwner == ingressSiteGateway
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
