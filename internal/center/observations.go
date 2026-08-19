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
	for _, value := range observations {
		value.AppKey = strings.TrimSpace(value.AppKey)
		value.Name = strings.TrimSpace(value.Name)
		value.Protocol = strings.TrimSpace(value.Protocol)
		value.AppProtocol = strings.TrimSpace(value.AppProtocol)
		value.Listen = strings.TrimSpace(value.Listen)
		if value.AppKey != threeXUIAppKey || !observedEndpointNamePattern.MatchString(value.Name) || (value.Protocol != "tcp" && value.Protocol != "udp") || value.AppProtocol == "" || value.Port < 1 || value.Port > 65535 {
			return errors.New("center: Agent reported an invalid application endpoint")
		}
		if value.Listen != "" && net.ParseIP(value.Listen) == nil {
			return errors.New("center: Agent reported an invalid application listen address")
		}
		if seen[value.Name] {
			return errors.New("center: Agent reported a duplicate application endpoint")
		}
		seen[value.Name] = true
	}
	var applicationID, siteID, serviceAddress string
	err := tx.QueryRowContext(ctx, `SELECT a.id, a.site_id, COALESCE(p.service_address, '127.0.0.1') FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.node_id = ? AND a.app_key = ? AND a.status = 'running'`, agentID, threeXUIAppKey).Scan(&applicationID, &siteID, &serviceAddress)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
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
		status := "ready"
		if !value.Enabled {
			status = "stopped"
		}
		endpoint := net.JoinHostPort(serviceAddress, strconv.Itoa(value.Port))
		serviceID := existing[value.Name]
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
		cleanup_pending = CASE WHEN dns_record_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
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
