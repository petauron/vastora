package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func (s *Store) reconcilePublicationDNS(ctx context.Context, id, gatewayID, provider string, revision int64) (bool, error) {
	// External publication reconciliation and cleanup share one lifecycle lock.
	// A stop may commit while an API request is in flight, but its cleanup waits
	// until this operation either records or compensates every remote resource.
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	var operationErr error
	successMessage := ""
	switch provider {
	case "cloudflare":
		operationErr = s.reconcileCloudflarePublication(ctx, id, revision)
		successMessage = "Cloudflare DNS record applied"
	case "headscale":
		operationErr = s.reconcileHeadscaleDNS(ctx)
		successMessage = "Headscale DNS records applied"
	default:
		return true, nil
	}
	if errors.Is(operationErr, errStalePublicationReconcile) {
		return false, nil
	}
	taskID := dnsTaskID(id, revision)
	if operationErr != nil {
		message := strings.TrimSpace(operationErr.Error())
		if len(message) > 1024 {
			message = message[:1024]
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped'`, message, s.now().UTC().Format(time.RFC3339Nano), id, revision); err != nil {
			return false, errors.Join(operationErr, err)
		}
		if err := s.recordStandaloneTaskEvent(ctx, taskID, gatewayID, "dns.record.apply", revision, "failed", message); err != nil {
			return false, errors.Join(operationErr, err)
		}
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE publications SET status = 'pending', last_error = '', updated_at = ? WHERE id = ? AND desired_revision = ? AND status = 'failed'`, s.now().UTC().Format(time.RFC3339Nano), id, revision); err != nil {
		return false, err
	}
	if err := s.recordStandaloneTaskEvent(ctx, taskID, gatewayID, "dns.record.apply", revision, "succeeded", successMessage); err != nil {
		return false, err
	}
	return true, nil
}

func dnsTaskID(publicationID string, revision int64) string {
	return fmt.Sprintf("dns-%s-r%d", publicationID, revision)
}

func (s *Store) publicationDNSRecord(ctx context.Context, publication PublicationView) (*DNSRecordInstruction, error) {
	record := &DNSRecordInstruction{Name: publication.Hostname}
	var address string
	switch publication.Kind {
	case publicationLAN:
		err := s.db.QueryRowContext(ctx, `SELECT n.lan_address FROM agent_network_profiles n JOIN publications p ON p.entry_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationHeadscale:
		err := s.db.QueryRowContext(ctx, `SELECT n.headscale_address FROM agent_network_profiles n JOIN publications p ON p.entry_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationPublic, publicationShared443:
		err := s.db.QueryRowContext(ctx, `SELECT n.public_address FROM agent_network_profiles n JOIN publications p ON p.entry_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationCloudflare:
		var tunnelID string
		err := s.db.QueryRowContext(ctx, `SELECT t.tunnel_id FROM cloudflare_tunnels t JOIN publications p ON p.entry_node_id = t.agent_id WHERE p.id = ?`, publication.ID).Scan(&tunnelID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		record.Type, record.Value, record.Proxy = "CNAME", tunnelID+".cfargotunnel.com", true
		return record, nil
	default:
		return nil, errors.New("center: stored publication kind is invalid")
	}
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil {
		return nil, errors.New("center: publication entry address must be IPv4")
	}
	record.Type, record.Value = "A", ip.String()
	return record, nil
}
