package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
)

func (s *Store) appendSystemGatewayRoutes(ctx context.Context, tx *sql.Tx, gatewayID string, state *gateway.DesiredState, listeners map[string]gateway.Listener) error {
	var headscaleEndpoint string
	err := tx.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&headscaleEndpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("center: read bundled Headscale endpoint: %w", err)
	}
	localCandidates, err := s.discoverNetworkCandidates(s.now().UTC())
	if err != nil {
		return fmt.Errorf("center: discover control-plane addresses: %w", err)
	}
	localAddresses := make(map[string]networking.Candidate, len(localCandidates))
	for _, candidate := range localCandidates {
		localAddresses[candidate.Address] = candidate
	}
	rows, err := tx.QueryContext(ctx, `SELECT address, interface_name, family, kind, observed_at FROM agent_network_candidates WHERE agent_id = ? ORDER BY kind, address`, gatewayID)
	if err != nil {
		return fmt.Errorf("center: read gateway addresses: %w", err)
	}
	coLocated := false
	publicAddress := ""
	headscaleAddress := ""
	for rows.Next() {
		var candidate networking.Candidate
		var observed string
		if err := rows.Scan(&candidate.Address, &candidate.Interface, &candidate.Family, &candidate.Kind, &observed); err != nil {
			rows.Close()
			return err
		}
		local, exists := localAddresses[candidate.Address]
		if !exists {
			continue
		}
		coLocated = true
		if publicAddress == "" && local.Kind == networking.KindPublic && candidate.Kind == networking.KindPublic {
			publicAddress = candidate.Address
		}
		if headscaleAddress == "" && local.Kind == networking.KindHeadscale && candidate.Kind == networking.KindHeadscale {
			headscaleAddress = candidate.Address
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !coLocated {
		return nil
	}
	if publicAddress == "" {
		return errors.New("center: the co-located Gateway does not report the public address used by bundled services")
	}
	if headscaleAddress == "" {
		return errors.New("center: the co-located Gateway does not report a Headscale address for private Center access")
	}
	var centerEndpoint string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&centerEndpoint); err != nil {
		return fmt.Errorf("center: read control-plane endpoint: %w", err)
	}
	centerHostname, err := gatewayEndpointHostname(centerEndpoint)
	if err != nil {
		return fmt.Errorf("center: control-plane endpoint: %w", err)
	}
	headscaleHostname, err := gatewayEndpointHostname(headscaleEndpoint)
	if err != nil {
		return fmt.Errorf("center: bundled Headscale endpoint: %w", err)
	}
	listeners["public"] = gateway.Listener{Kind: "public", Address: publicAddress, HTTPPort: 80, HTTPSPort: 443}
	listeners["headscale"] = gateway.Listener{Kind: "headscale", Address: headscaleAddress, HTTPPort: 80, HTTPSPort: 443}
	listeners["system"] = gateway.Listener{Kind: "system", Address: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443}
	state.Routes = append(state.Routes,
		gateway.Route{ID: "system-center", Hostname: centerHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "headscale", System: true},
		gateway.Route{ID: "system-headscale", Hostname: headscaleHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8081}}, TLSEnabled: true, ListenerKind: "public", System: true},
		gateway.Route{ID: "system-agent-bootstrap", Hostname: headscaleHostname, Path: "/install/agent.sh", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
		gateway.Route{ID: "system-center-local", Hostname: centerHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "system", System: true},
		gateway.Route{ID: "system-headscale-local", Hostname: headscaleHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8081}}, TLSEnabled: true, ListenerKind: "system", System: true},
	)
	return nil
}

func gatewayEndpointHostname(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", errors.New("HTTPS hostname is required")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func (s *Store) queueAllGatewayStates(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT gateway_node_id FROM gateway_components WHERE desired_status = 'running' ORDER BY gateway_node_id`)
	if err != nil {
		return err
	}
	var gatewayIDs []string
	for rows.Next() {
		var gatewayID string
		if err := rows.Scan(&gatewayID); err != nil {
			rows.Close()
			return err
		}
		gatewayIDs = append(gatewayIDs, gatewayID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, gatewayID := range gatewayIDs {
		if err := s.queueGatewayState(ctx, tx, gatewayID, s.now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
