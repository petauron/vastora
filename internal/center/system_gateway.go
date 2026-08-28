package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/petauron/vastora/internal/dockerruntime"
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
	binding, bindingConfigured, err := readSetupGatewayBinding(ctx, tx)
	if err != nil {
		return err
	}
	if !bindingConfigured {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT address, interface_name, kind, observed_at FROM agent_network_candidates WHERE agent_id = ? ORDER BY kind, address`, gatewayID)
	if err != nil {
		return fmt.Errorf("center: read gateway addresses: %w", err)
	}
	bindAddressReported := false
	headscaleAddress := ""
	for rows.Next() {
		var candidate networking.Candidate
		var observed string
		if err := rows.Scan(&candidate.Address, &candidate.Interface, &candidate.Kind, &observed); err != nil {
			rows.Close()
			return err
		}
		if candidate.Address == binding.BindAddress && (candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic) {
			bindAddressReported = true
		}
		if headscaleAddress == "" && candidate.Kind == networking.KindHeadscale {
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
	if !bindAddressReported {
		return nil
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
	listeners["public"] = gateway.Listener{Kind: "public", Address: binding.BindAddress, HTTPPort: 80, HTTPSPort: 443}
	listeners["headscale"] = gateway.Listener{Kind: "headscale", Address: headscaleAddress, HTTPPort: 80, HTTPSPort: 443}
	listeners["system"] = gateway.Listener{Kind: "system", Address: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443}
	state.Routes = append(state.Routes,
		gateway.Route{ID: "system-center", Hostname: centerHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "headscale", System: true},
		gateway.Route{ID: "system-headscale", Hostname: headscaleHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.HeadscaleAlias, Port: 8081}}, TLSEnabled: true, ListenerKind: "public", System: true},
		gateway.Route{ID: "system-agent-bootstrap", Hostname: headscaleHostname, Path: "/install/agent.sh", Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
		gateway.Route{ID: "system-agent-binary-bootstrap", Hostname: headscaleHostname, Path: "/api/v1/agent-binaries/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
		gateway.Route{ID: "system-center-local", Hostname: centerHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "system", System: true},
		gateway.Route{ID: "system-headscale-local", Hostname: headscaleHostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.HeadscaleAlias, Port: 8081}}, TLSEnabled: true, ListenerKind: "system", System: true},
	)
	centerAliases, err := readSystemEndpointAliases(ctx, tx, "center")
	if err != nil {
		return fmt.Errorf("center: read Center endpoint aliases: %w", err)
	}
	for _, alias := range centerAliases {
		hostname, err := gatewayEndpointHostname(alias.Endpoint)
		if err != nil {
			return fmt.Errorf("center: stored Center endpoint alias: %w", err)
		}
		if hostname == centerHostname {
			continue
		}
		key := strings.ReplaceAll(hostname, ".", "-")
		state.Routes = append(state.Routes,
			gateway.Route{ID: "system-center-alias-" + key, Hostname: hostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "headscale", System: true},
			gateway.Route{ID: "system-center-alias-local-" + key, Hostname: hostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "system", System: true},
		)
	}
	headscaleAliases, err := readSystemEndpointAliases(ctx, tx, "headscale")
	if err != nil {
		return fmt.Errorf("center: read Headscale endpoint aliases: %w", err)
	}
	for _, alias := range headscaleAliases {
		hostname, err := gatewayEndpointHostname(alias.Endpoint)
		if err != nil {
			return fmt.Errorf("center: stored Headscale endpoint alias: %w", err)
		}
		if hostname == headscaleHostname {
			continue
		}
		key := strings.ReplaceAll(hostname, ".", "-")
		state.Routes = append(state.Routes,
			gateway.Route{ID: "system-headscale-alias-" + key, Hostname: hostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.HeadscaleAlias, Port: 8081}}, TLSEnabled: true, ListenerKind: "public", System: true},
			gateway.Route{ID: "system-agent-bootstrap-alias-" + key, Hostname: hostname, Path: "/install/agent.sh", Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
			gateway.Route{ID: "system-agent-binary-bootstrap-alias-" + key, Hostname: hostname, Path: "/api/v1/agent-binaries/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.CenterAlias, Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
			gateway.Route{ID: "system-headscale-alias-local-" + key, Hostname: hostname, Protocol: "http", Upstreams: []gateway.Upstream{{Address: dockerruntime.HeadscaleAlias, Port: 8081}}, TLSEnabled: true, ListenerKind: "system", System: true},
		)
	}
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
	if err := s.queueAllGatewayStatesTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) queueAllGatewayStatesTx(ctx context.Context, tx *sql.Tx) error {
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
	return nil
}
