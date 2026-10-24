package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
)

const glivelaPilotCheckpointSchema = `CREATE TABLE glivela_pilot_checkpoint (
	id INTEGER PRIMARY KEY,
	endpoint_id TEXT NOT NULL UNIQUE REFERENCES meridian_endpoints(id),
	checkpoint_json BLOB NOT NULL CHECK(json_valid(checkpoint_json)),
	updated_at TEXT NOT NULL
)`

var errGlivelaPilotOwnsRuntime = errors.New("center: this entry runtime is owned by the scoped Glivela pilot")

func glivelaPilotOwnsEndpoint(ctx context.Context, tx *sql.Tx, endpointID string) (bool, error) {
	var owned bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM glivela_pilot_checkpoint WHERE endpoint_id=?)`, endpointID).Scan(&owned)
	return owned, err
}

func requireLegacyMeridianOwner(ctx context.Context, tx *sql.Tx, endpointID string) error {
	owned, err := glivelaPilotOwnsEndpoint(ctx, tx, endpointID)
	if err != nil {
		return err
	}
	if owned {
		return errGlivelaPilotOwnsRuntime
	}
	return nil
}

func requireLegacyMeridianApplicationOwner(ctx context.Context, tx *sql.Tx, applicationID string) error {
	var owned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM glivela_pilot_checkpoint p
		JOIN meridian_endpoints e ON e.id=p.endpoint_id WHERE e.application_id=?)`, applicationID).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return errGlivelaPilotOwnsRuntime
	}
	return nil
}

// recordGlivelaPilotReceipt is called only after authenticating the configured
// Glivela source. It does not accept an arbitrary endpoint from the report.
// The one-entry checkpoint and existing ledger advance in the same transaction.
// It does not project a legacy runtime, modify subscriptions, or own an account.
func (s *Store) recordGlivelaPilotReceipt(ctx context.Context, receipt meridianruntime.PilotReceipt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.recordGlivelaPilotReceiptInTx(ctx, tx, receipt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) recordGlivelaPilotReceiptInTx(ctx context.Context, tx *sql.Tx, receipt meridianruntime.PilotReceipt) error {
	var endpointID string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT p.endpoint_id,p.checkpoint_json FROM glivela_pilot_checkpoint p
		JOIN meridian_endpoints e ON e.id=p.endpoint_id WHERE e.application_id=?`, receipt.EntryID).Scan(&endpointID, &raw); err != nil {
		return err
	}
	var previous meridianruntime.PilotCounters
	if json.Unmarshal(raw, &previous) != nil {
		return errors.New("center: invalid Glivela pilot checkpoint")
	}
	// Check ownership against the current database as well as the signed-source
	// projection. A stale mapping must never write another entry's counters.
	var applicationID string
	if err := tx.QueryRowContext(ctx, `SELECT application_id FROM meridian_endpoints WHERE id=? AND status<>'retired'`, endpointID).Scan(&applicationID); err != nil {
		return err
	}
	if previous.EntryID != applicationID {
		return errors.New("center: Glivela pilot entry ownership changed")
	}
	now := s.now().UTC()
	next, err := meridianruntime.AdvancePilotCounters(previous, receipt, now)
	if err != nil {
		return err
	}
	for _, counter := range next.Totals {
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_credentials WHERE id=? AND endpoint_id=?)`, counter.CredentialID, endpointID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return errors.New("center: Glivela pilot credential ownership changed")
		}
	}
	// The checkpoint pins the sole ledger writer. Never silently reconcile an
	// unexpected legacy write by treating it as a native process restart.
	for _, counter := range previous.Totals {
		var up, down int64
		if err := tx.QueryRowContext(ctx, `SELECT raw_up_bytes,raw_down_bytes FROM meridian_usage_watermarks WHERE credential_id=?`,
			counter.CredentialID).Scan(&up, &down); err != nil {
			return err
		}
		if up != counter.UpBytes || down != counter.DownBytes {
			return errors.New("center: Glivela pilot ledger watermark changed outside receipt transaction")
		}
	}
	stamp := now.Format(time.RFC3339Nano)
	if previous.Quota != nil {
		var total, used int64
		var reset int
		var zone string
		if err := tx.QueryRowContext(ctx, `SELECT e.total_bytes,e.used_bytes,e.reset_day,site.timezone FROM meridian_endpoints e JOIN services service ON service.id=e.service_id JOIN sites site ON site.id=service.site_id WHERE e.id=?`, endpointID).Scan(&total, &used, &reset, &zone); err != nil {
			return err
		}
		if total != previous.Quota.TotalBytes || used != previous.Quota.UsedBytes || reset != previous.Quota.ResetDay || zone != previous.Quota.Timezone {
			return errors.New("center: staged quota changed outside its receipt transaction")
		}
	}
	if err := s.observeMeridianUsageInTx(ctx, tx, endpointID, next.Totals, stamp); err != nil {
		return err
	}
	if next.Quota != nil {
		q := next.Quota
		last, following := "", ""
		if !q.LastResetAt.IsZero() {
			last = q.LastResetAt.UTC().Format(time.RFC3339Nano)
		}
		if !q.NextResetAt.IsZero() {
			following = q.NextResetAt.UTC().Format(time.RFC3339Nano)
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET used_bytes=?,last_reset_at=?,next_reset_at=?,quota_applied_enabled=? WHERE id=? AND total_bytes=? AND reset_day=?`, q.UsedBytes, last, following, q.TotalBytes == 0 || q.UsedBytes < q.TotalBytes, endpointID, q.TotalBytes, q.ResetDay)
		if err != nil {
			return err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			return errors.New("center: staged quota policy changed outside its owner")
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE glivela_pilot_checkpoint SET checkpoint_json=?,updated_at=? WHERE endpoint_id=?`, encoded, stamp, endpointID); err != nil {
		return err
	}
	return nil
}

// The controller directory is mounted read-only at this fixed private path.
// A node never supplies a file path or its accounting identity. Polling is
// scoped to the activated checkpoint; the observation itself comes from
// Glivela's separately authenticated node exchange, not the Agent heartbeat.
func (s *Store) observeGlivelaPilotInTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	var endpointID string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT p.endpoint_id,p.checkpoint_json FROM glivela_pilot_checkpoint p
		JOIN meridian_endpoints e ON e.id=p.endpoint_id
		JOIN applications a ON a.id=e.application_id WHERE a.node_id=?`, agentID).Scan(&endpointID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var checkpoint meridianruntime.PilotCounters
	if json.Unmarshal(raw, &checkpoint) != nil {
		return errors.New("center: invalid Glivela pilot checkpoint")
	}
	statePath := s.glivelaPilotStatePath
	if statePath == "" {
		statePath = "/run/glivela-pilot/nodes.json"
	}
	var receipt meridianruntime.PilotReceipt
	var sourceErr error
	if checkpoint.Quota != nil {
		receipt, sourceErr = meridianruntime.ReadQuotaPilotReceipt(statePath, checkpoint, s.now().UTC())
	} else {
		receipt, sourceErr = meridianruntime.ReadUnlimitedPilotReceipt(statePath, checkpoint, s.now().UTC())
	}
	if sourceErr == nil {
		// Any ledger/checkpoint conflict is a real transaction error, not a
		// missing health sample. Do not commit partial accounting.
		if err := s.recordGlivelaPilotReceiptInTx(ctx, tx, receipt); err != nil {
			return err
		}
		return s.recordGlivelaPilotHealthInTx(ctx, tx, endpointID, checkpoint, receipt)
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	message := "Waiting for fresh authenticated Glivela pilot receipt"
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,last_error=?,updated_at=? WHERE id=?`,
		message, stamp, endpointID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_route_grants SET runtime_healthy=0,health_expires_unix_ms=0,
		last_error=?,updated_at=? WHERE endpoint_id=? AND status NOT IN ('revoked','revoking')`, message, stamp, endpointID)
	return err
}

// RunGlivelaPilotReceipts consumes each activated entry's pinned private
// receipt. Agent heartbeat cadence must not delay short-lived health
// evidence. Accounting still advances through the same atomic transaction.
func (s *Store) RunGlivelaPilotReceipts(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.refreshGlivelaPilotReceipt(step)
			cancel()
			if err != nil && ctx.Err() == nil && report != nil {
				report(err)
			}
		}
	}
}

func (s *Store) refreshGlivelaPilotReceipt(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT a.node_id FROM glivela_pilot_checkpoint p
		JOIN meridian_endpoints e ON e.id=p.endpoint_id
		JOIN applications a ON a.id=e.application_id ORDER BY a.node_id`)
	if err != nil {
		return err
	}
	var agents []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range agents {
		if err := s.refreshGlivelaPilotAgentReceipt(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Store) refreshGlivelaPilotAgentReceipt(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.observeGlivelaPilotInTx(ctx, tx, agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// A periodic refresh can lag a short-lived lease. Resolve the authenticated
// account's activated pilot from the latest private receipt in the subscription
// transaction, without extending its deadline or touching unrelated entries.
func (s *Store) refreshGlivelaPilotSubscriptionInTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.node_id FROM glivela_pilot_checkpoint p
		JOIN meridian_endpoints e ON e.id=p.endpoint_id
		JOIN applications a ON a.id=e.application_id
		WHERE EXISTS (SELECT 1 FROM meridian_credentials c WHERE c.endpoint_id=p.endpoint_id AND c.account_id=?)
		ORDER BY a.node_id`, accountID)
	if err != nil {
		return err
	}
	var agents []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range agents {
		if err := s.observeGlivelaPilotInTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recordGlivelaPilotHealthInTx(ctx context.Context, tx *sql.Tx, endpointID string,
	checkpoint meridianruntime.PilotCounters, receipt meridianruntime.PilotReceipt) error {
	now := s.now().UTC()
	readiness, err := meridianruntime.PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,egress_node_id,COALESCE(ipv4_egress_node_id,''),desired_revision,applied_revision
		FROM meridian_route_grants WHERE endpoint_id=? AND enabled=1 AND status NOT IN ('revoked','revoking')`, endpointID)
	if err != nil {
		return err
	}
	type route struct {
		id, primary, secondary string
		desired, applied       int64
	}
	var routes []route
	for rows.Next() {
		var r route
		if err := rows.Scan(&r.id, &r.primary, &r.secondary, &r.desired, &r.applied); err != nil {
			rows.Close()
			return err
		}
		routes = append(routes, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	for _, r := range routes {
		if r.desired != r.applied {
			return errors.New("center: legacy route configuration changed during Glivela pilot")
		}
		accepted := readiness[r.primary]
		if r.secondary != "" {
			accepted = accepted && readiness[r.secondary]
		}
		healthy, status, message := 0, "blocked", meridianRouteEvidenceUnavailable
		if accepted {
			healthy, status, message = 1, "ready", ""
		}
		if receipt.PublicationSuspended {
			healthy, status, message = 0, "blocked", "Glivela publication temporarily suspended"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET runtime_healthy=?,health_expires_unix_ms=?,
			status=?,last_error=?,updated_at=? WHERE id=? AND endpoint_id=?`,
			healthy, 0, status, message, stamp, r.id, endpointID); err != nil {
			return err
		}
	}
	healthy, status, message := 1, "ready", ""
	if receipt.PublicationSuspended {
		// The applied runtime remains ready; only publication is withheld.
		healthy, message = 0, "Glivela publication temporarily suspended"
	}
	result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=?,status=?,last_error=?,updated_at=?
		WHERE id=? AND status<>'retired' AND desired_revision=applied_revision AND applied_revision>0`, healthy, status, message, stamp, endpointID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("center: legacy entry configuration changed during Glivela pilot")
	}
	return nil
}
