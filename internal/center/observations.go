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
		// A worker reports its local database ids, while the Site controller owns
		// the stable cross-node ids used by clients and subscriptions.
		return nil
	}
	if role != threeXUIRoleMaster {
		return errors.New("center: running 3x-ui application has no topology role")
	}
	byApplication := map[string][]ApplicationEndpointObservation{masterApplicationID: {}}
	workerRows, err := tx.QueryContext(ctx, `SELECT worker_application_id FROM three_x_ui_nodes WHERE master_application_id = ? AND status = 'ready'`, masterApplicationID)
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
		if value.RemoteNodeID > 0 {
			err := tx.QueryRowContext(ctx, `SELECT worker_application_id FROM three_x_ui_nodes WHERE master_application_id = ? AND remote_node_id = ? AND status = 'ready'`, masterApplicationID, value.RemoteNodeID).Scan(&applicationID)
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

func (s *Store) reconcileObservedApplication(ctx context.Context, tx *sql.Tx, applicationID string, observations []ApplicationEndpointObservation, now time.Time, cleanups *[]publicationCleanup) error {
	var siteID, serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT a.site_id, COALESCE(p.service_address, '')
		FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.id = ?`, applicationID).Scan(&siteID, &serviceAddress); err != nil {
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
		if !value.Enabled {
			if err := s.stopServicePublications(ctx, tx, serviceID, now, cleanups); err != nil {
				return err
			}
		}
	}
	for _, serviceID := range existing {
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
