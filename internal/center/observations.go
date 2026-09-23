package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var observedEndpointNamePattern = regexp.MustCompile(`^inbound-[1-9][0-9]*$`)

func (s *Store) reconcileApplicationEndpoints(ctx context.Context, tx *sql.Tx, agentID string, observations []ApplicationEndpointObservation, now time.Time, cleanups *[]publicationCleanup) error {
	if len(observations) > 0 && strings.TrimSpace(observations[0].AppKey) == meridianAppKey {
		return s.reconcileMeridianEndpoints(ctx, tx, agentID, observations, now, cleanups)
	}
	if len(observations) == 0 {
		var meridianInstalled bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE node_id=? AND app_key=? AND status='running')`, agentID, meridianAppKey).Scan(&meridianInstalled); err != nil {
			return err
		}
		if meridianInstalled {
			return s.reconcileMeridianEndpoints(ctx, tx, agentID, observations, now, cleanups)
		}
	}
	seen := map[string]bool{}
	for index := range observations {
		value := &observations[index]
		value.AppKey = strings.TrimSpace(value.AppKey)
		value.Name = strings.TrimSpace(value.Name)
		value.Protocol = strings.TrimSpace(value.Protocol)
		value.AppProtocol = strings.TrimSpace(value.AppProtocol)
		value.Listen = strings.TrimSpace(value.Listen)
		value.InboundTag = strings.TrimSpace(value.InboundTag)
		if value.AppKey != threeXUIAppKey || !observedEndpointNamePattern.MatchString(value.Name) || (value.Protocol != "tcp" && value.Protocol != "udp") || value.AppProtocol == "" || value.Port < 1 || value.Port > 65535 {
			return errors.New("center: Agent reported an invalid application endpoint")
		}
		if value.Listen != "" && net.ParseIP(value.Listen) == nil {
			return errors.New("center: Agent reported an invalid application listen address")
		}
		if value.RemoteNodeID < 0 {
			return errors.New("center: Agent reported an invalid remote 3x-ui node")
		}
		if value.InboundTotalBytes < 0 || (value.InboundTag != "" && !validThreeXUIInboundTag(value.InboundTag)) {
			return errors.New("center: Agent reported invalid 3x-ui inbound traffic metadata")
		}
		key := strconv.Itoa(value.RemoteNodeID) + ":" + value.Name
		if seen[key] {
			return errors.New("center: Agent reported a duplicate application endpoint")
		}
		seen[key] = true
	}
	var masterApplicationID, role string
	err := tx.QueryRowContext(ctx, `SELECT id, role FROM applications WHERE node_id = ? AND app_key = ? AND status = 'running'`, agentID, threeXUIAppKey).Scan(&masterApplicationID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if role == threeXUIRoleWorker {
		// A worker reports its local database ids, while the global controller owns
		// the stable cross-node ids used by clients and subscriptions.
		return nil
	}
	if role != threeXUIRoleMaster {
		return errors.New("center: running 3x-ui application has no topology role")
	}
	byApplication := map[string][]ApplicationEndpointObservation{masterApplicationID: {}}
	workerRows, err := tx.QueryContext(ctx, `SELECT node.worker_application_id FROM three_x_ui_nodes node
		JOIN applications worker ON worker.id=node.worker_application_id
		WHERE node.master_application_id=? AND node.status='ready' AND worker.app_key=? AND worker.status='running'`, masterApplicationID, threeXUIAppKey)
	if err != nil {
		return err
	}
	for workerRows.Next() {
		var workerApplicationID string
		if err := workerRows.Scan(&workerApplicationID); err != nil {
			workerRows.Close()
			return err
		}
		byApplication[workerApplicationID] = []ApplicationEndpointObservation{}
	}
	if err := workerRows.Close(); err != nil {
		return err
	}
	for _, value := range observations {
		applicationID := masterApplicationID
		if value.RemoteNodeID == 0 && value.InboundTag != "" {
			// A heartbeat sampled before deletion may arrive after its acknowledgement.
			// Fence only that exact removed identity; a newly created tag is unaffected.
			var removed int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id=? AND kind=? AND state='succeeded' AND json_extract(input_json,'$.inboundTag')=? AND 'inbound-' || json_extract(input_json,'$.inboundId')=?`, applicationID, realityRemoveCommandKind, value.InboundTag, value.Name).Scan(&removed); err != nil {
				return err
			}
			if removed > 0 {
				continue
			}
		}
		if value.RemoteNodeID > 0 {
			err := tx.QueryRowContext(ctx, `SELECT node.worker_application_id FROM three_x_ui_nodes node
				JOIN applications worker ON worker.id=node.worker_application_id
				WHERE node.master_application_id=? AND node.remote_node_id=? AND node.status='ready'
				AND worker.app_key=? AND worker.status='running'`, masterApplicationID, value.RemoteNodeID, threeXUIAppKey).Scan(&applicationID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
		}
		byApplication[applicationID] = append(byApplication[applicationID], value)
	}
	for applicationID, values := range byApplication {
		if err := s.reconcileObservedApplication(ctx, tx, applicationID, values, now, cleanups); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileMeridianEndpoints(ctx context.Context, tx *sql.Tx, agentID string, observations []ApplicationEndpointObservation, now time.Time, cleanups *[]publicationCleanup) error {
	seenNames, seenTags := map[string]bool{}, map[string]bool{}
	for index := range observations {
		value := &observations[index]
		value.AppKey = strings.TrimSpace(value.AppKey)
		value.Name = strings.TrimSpace(value.Name)
		value.Protocol = strings.TrimSpace(value.Protocol)
		value.AppProtocol = strings.TrimSpace(value.AppProtocol)
		value.Listen = strings.TrimSpace(value.Listen)
		value.InboundTag = strings.TrimSpace(value.InboundTag)
		if value.AppKey != meridianAppKey || !observedEndpointNamePattern.MatchString(value.Name) || value.Protocol != "tcp" || value.AppProtocol != meridianEntryProtocol || value.Port < 1 || value.Port > 65535 || value.RemoteNodeID != 0 || value.InboundTotalBytes < 0 || !validThreeXUIInboundTag(value.InboundTag) {
			return errors.New("center: Agent reported an invalid Meridian endpoint")
		}
		if value.Listen != "" && net.ParseIP(value.Listen) == nil {
			return errors.New("center: Agent reported an invalid Meridian listen address")
		}
		if seenNames[value.Name] || seenTags[value.InboundTag] {
			return errors.New("center: Agent reported a duplicate Meridian endpoint")
		}
		seenNames[value.Name], seenTags[value.InboundTag] = true, true
	}
	var applicationID, role string
	err := tx.QueryRowContext(ctx, `SELECT id,role FROM applications WHERE node_id=? AND app_key=? AND status='running'`, agentID, meridianAppKey).Scan(&applicationID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if role != "" {
		return errors.New("center: Meridian application must not have a topology role")
	}
	return s.reconcileObservedMeridianApplication(ctx, tx, applicationID, observations, now, cleanups)
}

func (s *Store) reconcileObservedMeridianApplication(ctx context.Context, tx *sql.Tx, applicationID string, observations []ApplicationEndpointObservation, now time.Time, cleanups *[]publicationCleanup) error {
	var serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(p.service_address,'') FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id=a.node_id WHERE a.id=?`, applicationID).Scan(&serviceAddress); err != nil {
		return err
	}
	if serviceAddress == "" {
		_, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status=CASE WHEN status='retired' THEN status ELSE 'failed' END,last_error='Node network is recovering',updated_at=? WHERE application_id=?`, now.Format(time.RFC3339Nano), applicationID)
		return err
	}
	if net.ParseIP(serviceAddress) == nil {
		return errors.New("center: Meridian application node has an invalid service address")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM services WHERE application_id=? AND source='observed'`, applicationID)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	reconcilePublications := false
	for _, value := range observations {
		var serviceID, previousProtocol, previousEndpoint, previousStatus string
		err := tx.QueryRowContext(ctx, `SELECT service.id,service.app_protocol,service.endpoint,service.status
			FROM meridian_endpoints endpoint JOIN services service ON service.id=endpoint.service_id
			WHERE endpoint.application_id=? AND service.application_id=? AND service.source='observed'
			AND endpoint.status<>'retired' AND (endpoint.inbound_tag=? OR endpoint.hy2_inbound_tag=?)`, applicationID, applicationID, value.InboundTag, value.InboundTag).Scan(&serviceID, &previousProtocol, &previousEndpoint, &previousStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("center: observed Meridian endpoint does not match desired state")
		}
		if err != nil {
			return err
		}
		if !existing[serviceID] {
			return errors.New("center: observed Meridian endpoint is duplicated")
		}
		status := "stopped"
		if value.Enabled {
			status = "ready"
		}
		endpoint := net.JoinHostPort(serviceAddress, strconv.Itoa(value.Port))
		if _, err := tx.ExecContext(ctx, `UPDATE services SET protocol=?,container_port=?,host_port=?,endpoint=?,app_protocol=?,observed_listen=?,status=?,last_error='',updated_at=? WHERE id=?`, value.Protocol, value.Port, value.Port, endpoint, value.AppProtocol, value.Listen, status, stamp, serviceID); err != nil {
			return err
		}
		if value.Enabled && (previousProtocol != value.AppProtocol || previousEndpoint != endpoint || previousStatus != status) {
			reconcilePublications = true
		}
		result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET
			runtime_healthy=CASE WHEN applied_revision=desired_revision THEN ? ELSE runtime_healthy END,
			status=CASE WHEN status='retired' THEN status WHEN applied_revision<>desired_revision THEN status WHEN ?=1 THEN 'ready' ELSE 'failed' END,
			last_error=CASE WHEN applied_revision<>desired_revision THEN last_error WHEN ?=1 THEN '' ELSE 'Runtime endpoint is disabled' END,
			updated_at=? WHERE application_id=? AND service_id=? AND (inbound_tag=? OR hy2_inbound_tag=?)`, boolInt(value.Enabled), boolInt(value.Enabled), boolInt(value.Enabled), stamp, applicationID, serviceID, value.InboundTag, value.InboundTag)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("center: observed Meridian endpoint does not match desired state")
		}
		delete(existing, serviceID)
		if !value.Enabled {
			if err := s.stopServicePublications(ctx, tx, serviceID, now, cleanups); err != nil {
				return err
			}
		}
	}
	for _, serviceID := range existing {
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status='stopped',updated_at=? WHERE id=?`, stamp, serviceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status=CASE WHEN status='retired' THEN status ELSE 'failed' END,last_error='Runtime endpoint is missing',updated_at=? WHERE service_id=?`, stamp, serviceID); err != nil {
			return err
		}
		if err := s.stopServicePublications(ctx, tx, serviceID, now, cleanups); err != nil {
			return err
		}
	}
	if reconcilePublications {
		return s.reconcileApplicationPublications(ctx, tx, applicationID, now)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) reconcileObservedApplication(ctx context.Context, tx *sql.Tx, applicationID string, observations []ApplicationEndpointObservation, now time.Time, cleanups *[]publicationCleanup) error {
	var siteID, serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT a.site_id, COALESCE(p.service_address, '')
		FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.id = ?`, applicationID).Scan(&siteID, &serviceAddress); err != nil {
		return err
	}
	if serviceAddress == "" {
		// A controller can observe multiple workers. One worker temporarily
		// losing its private profile must not roll back everybody's heartbeat
		// or erase their inbounds. Retain identity and mark only this app unsure.
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'degraded', last_error = 'Node network is recovering', updated_at = ?
			WHERE application_id = ? AND source = 'observed' AND status NOT IN ('stopped', 'failed')`, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'degraded', last_error = 'Node network is recovering', updated_at = ?
			WHERE service_id IN (SELECT id FROM services WHERE application_id = ?) AND status IN ('ready', 'degraded')
			AND action_required = 0 AND (last_error = '' OR last_error = 'Node network is recovering')`, now.Format(time.RFC3339Nano), applicationID)
		return err
	}
	if net.ParseIP(serviceAddress) == nil {
		return errors.New("center: application node has an invalid service address")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM services WHERE application_id = ? AND source = 'observed'`, applicationID)
	if err != nil {
		return err
	}
	existing := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		existing[name] = id
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range observations {
		endpoint := net.JoinHostPort(serviceAddress, strconv.Itoa(value.Port))
		serviceID := existing[value.Name]
		guardStatus := ""
		if serviceID != "" && !value.Enabled && value.AppProtocol == "vless/tcp/reality" {
			if err := tx.QueryRowContext(ctx, `SELECT status FROM three_x_ui_reality_guards WHERE service_id = ?`, serviceID).Scan(&guardStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		status := observedThreeXUIServiceStatus(value.Enabled, value.AppProtocol, guardStatus)
		// A deliberately disabled VLESS inbound remains the stable node identity
		// when HY2 is selected. Keep its domain and subscription associations.
		var selectedHY2 int
		var managedProtocols int
		if serviceID != "" && !value.Enabled && value.AppProtocol == "vless/tcp/reality" {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(p.hy2_enabled=1 OR EXISTS(SELECT 1 FROM application_commands c WHERE c.kind='3xui.protocols.configure' AND (c.state IN ('pending','running') OR c.reconciliation_required=1) AND json_extract(c.input_json,'$.serviceId')=p.service_id AND json_extract(c.input_json,'$.hy2')=1)),0) FROM three_x_ui_node_protocols p WHERE p.service_id=?`, serviceID).Scan(&managedProtocols, &selectedHY2); err != nil {
				return err
			}
			if selectedHY2 > 0 {
				status = "ready"
			} else if managedProtocols > 0 {
				// Observations do not override an explicit protocol selection or
				// delete its DNS during a delayed post-update heartbeat.
				status = "degraded"
			}
		}
		if serviceID == "" {
			serviceID, err = randomToken(18)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'observed', ?, 0, ?, ?, ?, ?)`, serviceID, applicationID, siteID, value.Name, value.Protocol, value.Port, value.Port, endpoint, value.AppProtocol, value.Listen, status, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
				return fmt.Errorf("center: create observed application endpoint: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE services SET protocol = ?, container_port = ?, host_port = ?, endpoint = ?, app_protocol = ?, observed_listen = ?, status = ?, last_error = '', updated_at = ? WHERE id = ?`, value.Protocol, value.Port, value.Port, endpoint, value.AppProtocol, value.Listen, status, now.Format(time.RFC3339Nano), serviceID); err != nil {
			return err
		}
		if err := adoptObservedThreeXUIInboundPlan(ctx, tx, serviceID, value, now); err != nil {
			return err
		}
		delete(existing, value.Name)
		if !value.Enabled && managedProtocols == 0 {
			if err := s.stopServicePublications(ctx, tx, serviceID, now, cleanups); err != nil {
				return err
			}
		}
	}
	for _, serviceID := range existing {
		var removing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id=? AND kind IN (?, '3xui.protocols.configure') AND json_extract(input_json,'$.serviceId')=? AND (state IN ('pending','running') OR reconciliation_required=1)`, applicationID, realityRemoveCommandKind, serviceID).Scan(&removing); err != nil {
			return err
		}
		if removing > 0 {
			// Preserve the command's identity and retry entry until deletion is acknowledged.
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'stopped', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), serviceID); err != nil {
			return err
		}
		if err := s.stopServicePublications(ctx, tx, serviceID, now, cleanups); err != nil {
			return err
		}
	}
	return nil
}

func observedThreeXUIServiceStatus(enabled bool, appProtocol, guardStatus string) string {
	if enabled {
		return "ready"
	}
	if appProtocol == "vless/tcp/reality" && guardStatus != "" && guardStatus != "ready" {
		return "degraded"
	}
	return "stopped"
}

func adoptObservedThreeXUIInboundPlan(ctx context.Context, tx *sql.Tx, serviceID string, observation ApplicationEndpointObservation, now time.Time) error {
	if observation.AppProtocol != "vless/tcp/reality" || observation.InboundTag == "" {
		return nil
	}
	updatedAt := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(
		service_id, inbound_tag, total_bytes, reset_day, next_reset_at, last_reset_at,
		revision, status, retry_at, attempt, last_error, updated_at
	) VALUES(?, ?, ?, 0, '', '', 1, 'active', '', 0, '', ?)
	ON CONFLICT(service_id) DO NOTHING`, serviceID, observation.InboundTag, observation.InboundTotalBytes, updatedAt)
	if err != nil {
		return fmt.Errorf("center: adopt observed REALITY traffic plan: %w", err)
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		return nil
	}
	// A blank tag identifies a pre-management plan created by the v17 migration.
	// Once adopted, later heartbeats must not overwrite Center-owned quota settings.
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans
		SET inbound_tag = ?, total_bytes = ?, updated_at = ?
		WHERE service_id = ? AND inbound_tag = ''`, observation.InboundTag, observation.InboundTotalBytes, updatedAt, serviceID); err != nil {
		return fmt.Errorf("center: adopt legacy REALITY traffic plan: %w", err)
	}
	return nil
}

func (s *Store) stopServicePublications(ctx context.Context, tx *sql.Tx, serviceID string, now time.Time, cleanups *[]publicationCleanup) error {
	values, err := s.servicePublicationCleanups(ctx, tx, serviceID)
	if err != nil {
		return err
	}
	*cleanups = append(*cleanups, values...)
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT gateway_node_id FROM routes WHERE service_id = ?`, serviceID)
	if err != nil {
		return err
	}
	gateways := []string{}
	for rows.Next() {
		var gatewayID string
		if err := rows.Scan(&gatewayID); err != nil {
			rows.Close()
			return err
		}
		gateways = append(gateways, gatewayID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
		cleanup_pending = CASE WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
		cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE service_id = ? AND status <> 'stopped'`, now.Format(time.RFC3339Nano), serviceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE service_id = ?`, serviceID); err != nil {
		return err
	}
	for _, gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	return nil
}
