package center

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type publicationCleanup struct {
	ID          string
	Kind        string
	GatewayID   string
	DNSProvider string
	DNSRecordID string
	Revision    int64
}

func publicationCleanups(rows *sql.Rows) ([]publicationCleanup, error) {
	defer rows.Close()
	values := []publicationCleanup{}
	for rows.Next() {
		var value publicationCleanup
		if err := rows.Scan(&value.ID, &value.Kind, &value.GatewayID, &value.DNSProvider, &value.DNSRecordID, &value.Revision); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) servicePublicationCleanups(ctx context.Context, tx *sql.Tx, serviceID string) ([]publicationCleanup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, COALESCE(entry_node_id, ''), dns_provider, dns_record_id, desired_revision + 1
		FROM publications WHERE service_id = ? AND status <> 'stopped' ORDER BY id`, serviceID)
	if err != nil {
		return nil, err
	}
	return publicationCleanups(rows)
}

func (s *Store) applicationPublicationCleanups(ctx context.Context, tx *sql.Tx, applicationID string) ([]publicationCleanup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.kind, COALESCE(p.entry_node_id, ''), p.dns_provider, p.dns_record_id, p.desired_revision + 1
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.application_id = ? AND p.status <> 'stopped' ORDER BY p.id`, applicationID)
	if err != nil {
		return nil, err
	}
	return publicationCleanups(rows)
}

func (s *Store) cleanupStoppedPublications(ctx context.Context, values []publicationCleanup) error {
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()

	var cleanupErrors []error
	headscaleValues := []publicationCleanup{}
	for _, queued := range values {
		var value publicationCleanup
		err := s.db.QueryRowContext(ctx, `SELECT id, kind, COALESCE(entry_node_id, ''), dns_provider, dns_record_id, desired_revision
			FROM publications WHERE id = ? AND status = 'stopped' AND cleanup_pending = 1`, queued.ID).Scan(&value.ID, &value.Kind, &value.GatewayID, &value.DNSProvider, &value.DNSRecordID, &value.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if value.DNSRecordID != "" || value.Kind == publicationCloudflare {
			operationErr := s.removeCloudflarePublication(ctx, value.ID, value.Kind, value.GatewayID, value.DNSRecordID)
			if recordErr := s.finishPublicationCleanup(ctx, value, "cloudflare", operationErr); recordErr != nil {
				cleanupErrors = append(cleanupErrors, recordErr)
			}
			continue
		}
		if value.DNSProvider == "headscale" {
			headscaleValues = append(headscaleValues, value)
		}
	}
	if len(headscaleValues) != 0 {
		err := s.reconcileHeadscaleDNS(ctx)
		for _, value := range headscaleValues {
			if recordErr := s.finishPublicationCleanup(ctx, value, "headscale", err); recordErr != nil {
				cleanupErrors = append(cleanupErrors, recordErr)
			}
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *Store) finishPublicationCleanup(ctx context.Context, value publicationCleanup, provider string, operationErr error) error {
	now := s.now().UTC()
	message := ""
	if operationErr != nil {
		message = strings.TrimSpace(operationErr.Error())
		if len(message) > 1024 {
			message = message[:1024]
		}
		var attempt int
		if err := s.db.QueryRowContext(ctx, `SELECT cleanup_attempt FROM publications WHERE id = ?`, value.ID).Scan(&attempt); err != nil {
			return errors.Join(operationErr, err)
		}
		retryAt := now.Add(publicationCleanupBackoff(attempt + 1))
		if _, err := s.db.ExecContext(ctx, `UPDATE publications SET cleanup_pending = 1, cleanup_attempt = cleanup_attempt + 1, cleanup_retry_at = ?, last_error = ?, updated_at = ? WHERE id = ? AND status = 'stopped'`, retryAt.Format(time.RFC3339Nano), message, now.Format(time.RFC3339Nano), value.ID); err != nil {
			return errors.Join(operationErr, err)
		}
	} else if _, err := s.db.ExecContext(ctx, `UPDATE publications SET cleanup_pending = 0, cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE id = ? AND status = 'stopped'`, now.Format(time.RFC3339Nano), value.ID); err != nil {
		return err
	}
	return s.recordDNSRemoval(ctx, value.ID, value.GatewayID, value.Revision, provider, operationErr)
}

func publicationCleanupBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Duration(1<<(attempt-1)) * time.Minute
}

func (s *Store) recordDNSRemoval(ctx context.Context, publicationID, agentID string, revision int64, provider string, operationErr error) error {
	event, message := "succeeded", provider+" DNS record removed"
	if operationErr != nil {
		event, message = "failed", operationErr.Error()
	}
	return s.recordStandaloneTaskEvent(ctx, dnsTaskID(publicationID, revision), agentID, "dns.record.remove", revision, event, message)
}

func (s *Store) retryPublicationCleanups(ctx context.Context) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, COALESCE(entry_node_id, ''), dns_provider, dns_record_id, desired_revision
		FROM publications
		WHERE status = 'stopped' AND cleanup_pending = 1 AND (cleanup_retry_at = '' OR cleanup_retry_at <= ?)
		ORDER BY cleanup_retry_at, updated_at, id LIMIT 20`, now)
	if err != nil {
		return err
	}
	values, err := publicationCleanups(rows)
	if err != nil {
		return err
	}
	return s.cleanupStoppedPublications(ctx, values)
}

func (s *Store) RunPublicationCleanup(ctx context.Context, interval time.Duration, report func(error)) {
	if interval < time.Minute {
		interval = time.Minute
	}
	run := func() {
		if err := s.retryPublicationCleanups(ctx); err != nil && report != nil {
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
