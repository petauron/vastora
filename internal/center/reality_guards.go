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
)

const realityGuardRevalidationInterval = 6 * time.Hour

// RunRealityGuardRevalidation re-proves every ready guard at Center startup
// and then on the configured interval. Revalidation deliberately withdraws
// the HAProxy route before the Agent disables and rechecks the inbound, so an
// unavailable external dependency can never leave an unverified service
// published.
func (s *Store) RunRealityGuardRevalidation(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = realityGuardRevalidationInterval
	}
	run := func() {
		if err := s.quarantineReadyRealityGuards(ctx); err != nil {
			if report != nil && !errors.Is(err, context.Canceled) {
				report(err)
			}
			return
		}
		if err := s.startRealityGuardHardening(ctx); err != nil && report != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
	}
	run()
	revalidationTicker := time.NewTicker(interval)
	retryTicker := time.NewTicker(time.Minute)
	defer revalidationTicker.Stop()
	defer retryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-revalidationTicker.C:
			run()
		case <-retryTicker.C:
			if err := s.startRealityGuardHardening(ctx); err != nil && report != nil && !errors.Is(err, context.Canceled) {
				report(err)
			}
		}
	}
}

func (s *Store) quarantineReadyRealityGuards(ctx context.Context) error {
	return s.quarantineReadyRealityGuardsForAgent(ctx, "")
}

func (s *Store) quarantineReadyRealityGuardsForAgent(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT publication.entry_node_id
		FROM three_x_ui_reality_guards guard
		JOIN services service ON service.id = guard.service_id
		JOIN applications application ON application.id = service.application_id
		JOIN publications publication ON publication.service_id = guard.service_id
		WHERE guard.status = 'ready' AND publication.entry_node_id IS NOT NULL
		 AND publication.status <> 'stopped' AND (? = '' OR application.node_id = ?)`, agentID, agentID)
	if err != nil {
		return err
	}
	listenerNodes := []string{}
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		listenerNodes = append(listenerNodes, nodeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_reality_guards
		SET status = 'action_required', last_error = 'scheduled REALITY guard revalidation', updated_at = ?
		WHERE status = 'ready' AND (? = '' OR service_id IN (
		 SELECT service.id FROM services service JOIN applications application ON application.id = service.application_id
		 WHERE application.node_id = ?
	))`, nowText, agentID, agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'degraded',
		last_error = 'scheduled REALITY guard revalidation', updated_at = ?
		WHERE id IN (SELECT service_id FROM three_x_ui_reality_guards WHERE status = 'action_required'
		 AND last_error = 'scheduled REALITY guard revalidation') AND status <> 'stopped'`, nowText); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE publication_id IN (
		SELECT publication.id FROM publications publication
		JOIN three_x_ui_reality_guards guard ON guard.service_id = publication.service_id
		WHERE guard.status = 'action_required' AND guard.last_error = 'scheduled REALITY guard revalidation'
	)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped',
		last_error = 'REALITY guard revalidation is in progress', updated_at = ?
		WHERE service_id IN (SELECT service_id FROM three_x_ui_reality_guards
		 WHERE status = 'action_required' AND last_error = 'scheduled REALITY guard revalidation')
		AND status <> 'stopped'`, nowText); err != nil {
		return err
	}
	for _, nodeID := range listenerNodes {
		if err := s.queueNodeListenerState(ctx, tx, nodeID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) startRealityGuardHardening(ctx context.Context) error {
	if err := s.queueRealityGuardListenerIsolation(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT service_id FROM three_x_ui_reality_guards WHERE status = 'action_required' ORDER BY updated_at, service_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	serviceIDs := []string{}
	for rows.Next() {
		var serviceID string
		if err := rows.Scan(&serviceID); err != nil {
			return err
		}
		serviceIDs = append(serviceIDs, serviceID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, serviceID := range serviceIDs {
		if _, err := s.queueRealityGuardHardening(ctx, serviceID); err != nil {
			_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE three_x_ui_reality_guards SET last_error = ?, updated_at = ? WHERE service_id = ? AND status = 'action_required'`, applicationCommandFailureMessage(err), s.now().UTC().Format(time.RFC3339Nano), serviceID)
		}
	}
	return nil
}

func (s *Store) queueRealityGuardListenerIsolation(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT publication.entry_node_id
		FROM publications publication
		JOIN three_x_ui_reality_guards guard ON guard.service_id = publication.service_id
		WHERE publication.entry_node_id IS NOT NULL
		 AND publication.status = 'stopped'
		 AND publication.last_error = 'REALITY guard requires hardening before publication'
		 AND guard.status <> 'ready'`)
	if err != nil {
		return err
	}
	listenerNodes := []string{}
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		listenerNodes = append(listenerNodes, nodeID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC()
	for _, nodeID := range listenerNodes {
		if err := s.queueNodeListenerState(ctx, tx, nodeID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) queueRealityGuardHardening(ctx context.Context, serviceID string) (ApplicationCommandView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var applicationID, siteID, serviceName, displayName, regionCode, targetHost, serverName, inboundTag string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT service.application_id, service.site_id, service.name, service.display_name, service.region_code,
		guard.target_host, guard.server_name, guard.revision,
		COALESCE(plan.inbound_tag, COALESCE(json_extract(command.input_json, '$.inboundTag'), ''))
		FROM services service
		JOIN three_x_ui_reality_guards guard ON guard.service_id = service.id
		LEFT JOIN three_x_ui_inbound_plans plan ON plan.service_id = service.id
		LEFT JOIN application_commands command ON command.application_id = service.application_id
		 AND command.kind = '3xui.reality.create'
		 AND CAST(json_extract(command.result_json, '$.inboundId') AS INTEGER) = CAST(substr(service.name, 9) AS INTEGER)
		WHERE service.id = ? AND service.app_protocol = 'vless/tcp/reality'
		 AND service.status <> 'stopped' AND guard.status = 'action_required'`, serviceID).Scan(&applicationID, &siteID, &serviceName, &displayName, &regionCode, &targetHost, &serverName, &revision, &inboundTag); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: REALITY service does not require hardening")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	targetHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(targetHost)), ".")
	serverName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(serverName)), ".")
	if strings.TrimSpace(inboundTag) == "" {
		return ApplicationCommandView{}, errors.New("center: existing REALITY service has no deterministic inbound tag")
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(serviceName, "inbound-"))
	if err != nil || inboundID < 1 {
		return ApplicationCommandView{}, errors.New("center: existing REALITY service has an invalid inbound identifier")
	}
	var targetAgentID, role, targetAddress, targetPublicAddress, applicationStatus string
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id, application.role, application.status,
		COALESCE(profile.service_address, ''), COALESCE(profile.public_address, '')
		FROM applications application LEFT JOIN agent_network_profiles profile ON profile.agent_id = application.node_id
		WHERE application.id = ?`, applicationID).Scan(&targetAgentID, &role, &applicationStatus, &targetAddress, &targetPublicAddress); err != nil {
		return ApplicationCommandView{}, err
	}
	if applicationStatus != "running" || net.ParseIP(targetAddress) == nil {
		return ApplicationCommandView{}, errors.New("center: REALITY hardening requires a reachable 3x-ui node")
	}
	agentID, targetNodeID := targetAgentID, 0
	if role == threeXUIRoleWorker {
		if err := tx.QueryRowContext(ctx, `SELECT master.node_id, node.remote_node_id
			FROM three_x_ui_nodes node JOIN applications master ON master.id = node.master_application_id
			WHERE node.worker_application_id = ? AND node.status = 'ready' AND master.status = 'running'`, applicationID).Scan(&agentID, &targetNodeID); err != nil {
			return ApplicationCommandView{}, errors.New("center: this VLESS node is not connected to the Site 3x-ui controller")
		}
	}
	var panelConfig []byte
	if err := tx.QueryRowContext(ctx, `SELECT config_json FROM deployments WHERE application_id = ? AND operation IN ('install', 'upgrade', 'configure') AND state = 'succeeded' ORDER BY created_at DESC, rowid DESC LIMIT 1`, applicationID).Scan(&panelConfig); err != nil {
		return ApplicationCommandView{}, errors.New("center: target VLESS node configuration is unavailable")
	}
	var targetSettings struct {
		PanelPort int `json:"panel_port"`
	}
	if json.Unmarshal(panelConfig, &targetSettings) != nil || targetSettings.PanelPort < 1024 || targetSettings.PanelPort > 65535 {
		return ApplicationCommandView{}, errors.New("center: target VLESS node configuration is invalid")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui controller already has an operation in progress")
	}
	var gatewayID, connectHostname, dnsProvider string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(entry_node_id, ''), hostname, dns_provider FROM publications
		WHERE service_id = ? AND kind = 'public_shared_443' ORDER BY updated_at DESC LIMIT 1`, serviceID).Scan(&gatewayID, &connectHostname, &dnsProvider)
	if gatewayID == "" {
		gatewayID = agentID
	}
	if dnsProvider == "" {
		dnsProvider = "manual"
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	nextRevision := revision + 1
	task := RealityCommandTask{
		Action: "harden", ServiceID: serviceID, GuardRevision: nextRevision,
		RegionCode: regionCode, DisplayName: displayName, InboundID: inboundID, InboundTag: inboundTag,
		ConnectHostname: connectHostname, DNSProvider: dnsProvider, TargetHost: targetHost, ServerName: serverName,
		TargetApplicationID: applicationID, TargetAddress: targetAddress, TargetPublicAddress: targetPublicAddress,
		TargetPanelPort: targetSettings.PanelPort, TargetNodeID: targetNodeID,
	}
	encoded, _ := json.Marshal(task)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, applicationID, siteID, displayName, agentID, gatewayID, realityHardenCommandKind, encoded, now, now); err != nil {
		return ApplicationCommandView{}, fmt.Errorf("center: queue REALITY hardening: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_reality_guards SET revision = ?, status = 'hardening', last_error = '', updated_at = ? WHERE service_id = ? AND revision = ? AND status = 'action_required'`, nextRevision, now, serviceID, revision); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", nextRevision, "queued", "REALITY fallback guard hardening queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) completeRealityHardenCommand(ctx context.Context, tx *sql.Tx, taskID, agentID, applicationID, gatewayID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input RealityCommandTask
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &input) != nil || input.Action != "harden" || input.ServiceID == "" || input.GuardRevision < 1 {
		return errors.New("center: stored REALITY hardening operation is invalid")
	}
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid REALITY hardening result"
		} else {
			result := envelope.ApplicationCommand
			expectedInboundTag := input.InboundTag
			if input.TargetNodeID > 0 {
				expectedInboundTag = "n" + strconv.Itoa(input.TargetNodeID) + "-" + input.InboundTag
			}
			if result.Action != "harden" || result.InboundID != input.InboundID || result.Listen != input.TargetAddress || result.Port != centerThreeXUIRealityPort || (result.InboundTag != input.InboundTag && result.InboundTag != expectedInboundTag) || result.TargetHost != input.TargetHost || result.ServerName != input.ServerName || !validRealityTargetProof(*result) || result.GuardStatus != "ready" || !result.ProxyProtocol {
				succeeded = false
				taskError = "center: Agent returned an unsafe REALITY hardening result"
			}
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, event, message := "succeeded", "succeeded", "REALITY fallback guard hardened"
	resultJSON := []byte(`{}`)
	if succeeded {
		result := envelope.ApplicationCommand
		resultJSON, _ = json.Marshal(result)
		updated, err := tx.ExecContext(ctx, `UPDATE three_x_ui_reality_guards SET target_host = ?, target_ip = ?, server_name = ?, node_asn = ?, target_asn = ?, cdn_provider = ?, companion_inbound_id = 0, companion_tag = '', companion_port = 0, status = 'ready', verified_at = ?, last_error = '', updated_at = ? WHERE service_id = ? AND revision = ? AND status = 'hardening'`, result.TargetHost, result.TargetIP, result.ServerName, result.NodeASN, result.TargetASN, result.CDNProvider, now, now, input.ServiceID, input.GuardRevision)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			succeeded = false
			taskError = "center: REALITY guard changed while hardening"
		} else if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'ready', last_error = '', updated_at = ? WHERE id = ? AND application_id = ? AND status <> 'stopped'`, now, input.ServiceID, applicationID); err != nil {
			return err
		}
	}
	if !succeeded {
		if taskError == "" {
			taskError = "REALITY fallback guard hardening failed"
		}
		state, event, message = "failed", "failed", taskError
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_reality_guards SET status = 'action_required', last_error = ?, updated_at = ? WHERE service_id = ? AND revision = ?`, taskError, now, input.ServiceID, input.GuardRevision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'degraded', last_error = ?, updated_at = ? WHERE id = ? AND status <> 'stopped'`, taskError, now, input.ServiceID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", input.GuardRevision, event, message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil || !succeeded {
		return err
	}
	if input.ConnectHostname == "" {
		return nil
	}
	_, publicationErr := s.CreatePublication(ctx, PublicationInput{ServiceID: input.ServiceID, Kind: publicationShared443, Ingress: PublicationIngress{Owner: ingressApplicationNode}, Hostname: input.ConnectHostname, SNIHostname: input.ServerName, DNSProvider: input.DNSProvider})
	warning := ""
	if publicationErr != nil {
		warning = "center: restore hardened REALITY access entry: " + publicationErr.Error()
	}
	// Hardening is already durable and the inbound is protected. Keep access
	// restoration as a recoverable projection so a transient Center write cannot
	// make the Agent replay the 3x-ui mutation.
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE application_commands SET error = ?, updated_at = ? WHERE id = ? AND state = 'succeeded'`, warning, s.now().UTC().Format(time.RFC3339Nano), taskID)
	return nil
}
