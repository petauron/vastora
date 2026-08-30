package center

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const systemEndpointAliasRetirementLead = 7 * 24 * time.Hour

var errSystemEndpointDNSOwnership = errors.New("center: system endpoint DNS ownership cannot be verified")

func (s *Store) beginDueSystemEndpointAliasRetirements(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE system_endpoint_aliases
		SET lifecycle_state = 'retiring', last_error = '', updated_at = ?
		WHERE lifecycle_state = 'active' AND transition_id IN (
			SELECT transition_id FROM system_endpoint_aliases
			WHERE lifecycle_state = 'active' AND (
				(retire_after <> '' AND julianday(retire_after) <= julianday(?)) OR
				(kind = 'center' AND certificate_not_after <> '' AND julianday(certificate_not_after) <= julianday(?))
			)
		)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(systemEndpointAliasRetirementLead).Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("center: begin due system endpoint retirement: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return false, nil
	}
	if err := s.queueAllGatewayStatesTx(ctx, tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) beginSystemEndpointAliasRetirement(ctx context.Context, transitionID string) error {
	transitionID = strings.TrimSpace(transitionID)
	if transitionID == "" {
		return errors.New("center: system endpoint transition ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_endpoint_aliases WHERE transition_id = ?`, transitionID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("center: system endpoint transition was not found")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE system_endpoint_aliases SET lifecycle_state = 'retiring', last_error = '', updated_at = ?
		WHERE transition_id = ? AND lifecycle_state <> 'retiring'`, now.Format(time.RFC3339Nano), transitionID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 0 {
		if err := s.queueAllGatewayStatesTx(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) retiringSystemEndpointTransitions(ctx context.Context) ([]systemEndpointAlias, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, endpoint, COALESCE(certificate_secret_id, ''), certificate_not_after,
		transition_id, lifecycle_state, retire_after, dns_account_id, dns_zone_id, dns_record_id, dns_record_type, dns_record_content, last_error
		FROM system_endpoint_aliases WHERE kind = 'headscale' AND lifecycle_state = 'retiring' ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []systemEndpointAlias{}
	for rows.Next() {
		var value systemEndpointAlias
		var certificateNotAfter, retireAfter string
		if err := rows.Scan(&value.Kind, &value.Endpoint, &value.CertificateSecretID, &certificateNotAfter,
			&value.TransitionID, &value.LifecycleState, &retireAfter, &value.DNSAccountID, &value.DNSZoneID,
			&value.DNSRecordID, &value.DNSRecordType, &value.DNSRecordContent, &value.LastError); err != nil {
			return nil, err
		}
		if retireAfter != "" {
			value.RetireAfter, err = time.Parse(time.RFC3339Nano, retireAfter)
			if err != nil {
				return nil, errors.New("center: stored system endpoint retirement deadline is invalid")
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) systemGatewayStatesReady(ctx context.Context) (bool, error) {
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_components components
		LEFT JOIN gateway_states states ON states.gateway_node_id = components.gateway_node_id
		WHERE components.desired_status = 'running' AND (
			states.gateway_node_id IS NULL OR states.status <> 'ready' OR states.applied_revision <> states.desired_revision
		)`).Scan(&pending)
	return pending == 0, err
}

func (s *Store) failSystemEndpointAliasRetirement(ctx context.Context, transitionID string, cause error) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE system_endpoint_aliases SET lifecycle_state = 'failed', last_error = ?, updated_at = ?
		WHERE transition_id = ?`, message, s.now().UTC().Format(time.RFC3339Nano), transitionID)
	return err
}

func (s *Store) recordSystemEndpointAliasRetirementError(ctx context.Context, transitions []systemEndpointAlias, cause error) {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, transition := range transitions {
		_, _ = s.db.ExecContext(ctx, `UPDATE system_endpoint_aliases SET last_error = ?, updated_at = ?
			WHERE transition_id = ? AND lifecycle_state = 'retiring'`, message, now, transition.TransitionID)
	}
}

func (s *Store) clearSystemEndpointAliasRetirementErrors(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE system_endpoint_aliases SET last_error = '', updated_at = ?
		WHERE lifecycle_state = 'retiring' AND last_error <> ''`, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) finalizeSystemEndpointAliasRetirement(ctx context.Context, transitionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT certificate_secret_id FROM system_endpoint_aliases
		WHERE transition_id = ? AND certificate_secret_id IS NOT NULL AND certificate_secret_id <> ''`, transitionID)
	if err != nil {
		return err
	}
	var secretIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		secretIDs = append(secretIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM system_endpoint_aliases WHERE transition_id = ?`, transitionID); err != nil {
		return err
	}
	for _, id := range secretIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ? AND NOT EXISTS (
			SELECT 1 FROM system_endpoint_aliases WHERE certificate_secret_id = ?
		) AND NOT EXISTS (SELECT 1 FROM settings WHERE key = ? AND value = ?)`, id, id, systemCenterCertificateSecretSetting, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) deleteOwnedSystemEndpointDNS(ctx context.Context, alias systemEndpointAlias) error {
	if alias.DNSAccountID == "" || alias.DNSZoneID == "" || alias.DNSRecordID == "" || alias.DNSRecordType == "" || alias.DNSRecordContent == "" {
		return fmt.Errorf("%w: old Headscale DNS ownership metadata is missing", errSystemEndpointDNSOwnership)
	}
	client, zone, err := s.cloudflareForZone(ctx, alias.DNSZoneID)
	if err != nil {
		return err
	}
	if zone.AccountID != alias.DNSAccountID {
		return fmt.Errorf("%w: old Headscale DNS account changed", errSystemEndpointDNSOwnership)
	}
	hostname, err := gatewayEndpointHostname(alias.Endpoint)
	if err != nil {
		return err
	}
	records, err := client.listDNSRecords(ctx, hostname)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 || records[0].ID != alias.DNSRecordID || records[0].Type != alias.DNSRecordType ||
		records[0].Content != alias.DNSRecordContent || records[0].Proxied || !strings.EqualFold(strings.TrimSuffix(records[0].Name, "."), hostname) {
		return fmt.Errorf("%w: old Headscale DNS record %s changed outside Vastora; it was not deleted", errSystemEndpointDNSOwnership, hostname)
	}
	if err := client.deleteDNSRecord(ctx, alias.DNSRecordID); err != nil && !cloudflareResourceNotFound(err) {
		return err
	}
	return nil
}

func (s *Server) MaintainSystemEndpointAliases(ctx context.Context) error {
	if s.infrastructure == nil {
		return errors.New("center: system endpoint maintenance requires the deployment helper")
	}
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()
	return s.maintainSystemEndpointAliasesLocked(ctx)
}

func (s *Server) maintainSystemEndpointAliasesLocked(ctx context.Context) error {
	if _, err := s.store.beginDueSystemEndpointAliasRetirements(ctx); err != nil {
		return err
	}
	transitions, err := s.store.retiringSystemEndpointTransitions(ctx)
	if err != nil || len(transitions) == 0 {
		return err
	}
	snapshot, configured, err := s.builtinHeadscaleReconcileSnapshot(ctx)
	if err != nil {
		s.store.recordSystemEndpointAliasRetirementError(context.WithoutCancel(ctx), transitions, err)
		return err
	}
	if !configured {
		err := errors.New("center: bundled Headscale is not configured")
		s.store.recordSystemEndpointAliasRetirementError(context.WithoutCancel(ctx), transitions, err)
		return err
	}
	if err := s.infrastructure.ReconcileHeadscale(ctx, snapshot.Request); err != nil {
		s.store.recordSystemEndpointAliasRetirementError(context.WithoutCancel(ctx), transitions, err)
		return err
	}
	if err := s.store.reconcileHeadscaleDNS(ctx); err != nil {
		s.store.recordSystemEndpointAliasRetirementError(context.WithoutCancel(ctx), transitions, err)
		return err
	}
	if err := s.store.clearSystemEndpointAliasRetirementErrors(ctx); err != nil {
		return err
	}
	ready, err := s.store.systemGatewayStatesReady(ctx)
	if err != nil || !ready {
		return err
	}
	var cleanupErrors []error
	for _, transition := range transitions {
		if err := s.store.deleteOwnedSystemEndpointDNS(ctx, transition); err != nil {
			if errors.Is(err, errSystemEndpointDNSOwnership) {
				_ = s.store.failSystemEndpointAliasRetirement(context.WithoutCancel(ctx), transition.TransitionID, err)
			} else {
				s.store.recordSystemEndpointAliasRetirementError(context.WithoutCancel(ctx), []systemEndpointAlias{transition}, err)
			}
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if err := s.store.finalizeSystemEndpointAliasRetirement(ctx, transition.TransitionID); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *Server) RetireSystemEndpointAliases(ctx context.Context, transitionID string) error {
	if s.infrastructure == nil {
		return errors.New("center: system endpoint retirement requires the deployment helper")
	}
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()
	if err := s.store.beginSystemEndpointAliasRetirement(ctx, transitionID); err != nil {
		return err
	}
	return s.maintainSystemEndpointAliasesLocked(ctx)
}

func (s *Server) RunSystemEndpointAliasMaintenance(ctx context.Context, interval time.Duration, report func(error)) {
	if interval < time.Minute {
		interval = time.Minute
	}
	run := func() {
		if err := s.MaintainSystemEndpointAliases(ctx); err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
