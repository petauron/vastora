package center

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// An entry cap is shared by every account and route on the endpoint. It is
// independent of the account-wide quota carried by each credential.
func meridianEndpointQuotaEnabledInTx(ctx context.Context, tx *sql.Tx, endpointID string) (bool, error) {
	var total, used int64
	if err := tx.QueryRowContext(ctx, `SELECT total_bytes,used_bytes FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&total, &used); err != nil {
		return false, err
	}
	if total < 0 || used < 0 {
		return false, errors.New("center: stored Meridian entry traffic is invalid")
	}
	return total == 0 || used < total, nil
}

func (s *Store) markMeridianEndpointQuotaBoundaryChanged(ctx context.Context, tx *sql.Tx, endpointID, nowText string) error {
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
		WHERE id=? AND status<>'retired'`, nowText, endpointID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Meridian entry traffic boundary has no active endpoint")
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,
		status=CASE WHEN status IN ('revoking','revoked') THEN status ELSE 'pending' END,last_error='',updated_at=?
		WHERE endpoint_id=? AND status<>'revoked'`, nowText, endpointID)
	return err
}

func (s *Store) resetDueMeridianEndpoints(ctx context.Context, tx *sql.Tx) error {
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT endpoint.id,endpoint.reset_day,site.timezone
		FROM meridian_endpoints endpoint JOIN services service ON service.id=endpoint.service_id
		JOIN sites site ON site.id=service.site_id
		WHERE endpoint.reset_day>0 AND endpoint.next_reset_at<>'' AND julianday(endpoint.next_reset_at)<=julianday(?)
		AND endpoint.status<>'retired' ORDER BY endpoint.id`, nowText)
	if err != nil {
		return err
	}
	type dueEndpoint struct {
		id, timezone string
		resetDay     int
	}
	due := []dueEndpoint{}
	for rows.Next() {
		var endpoint dueEndpoint
		if err := rows.Scan(&endpoint.id, &endpoint.resetDay, &endpoint.timezone); err != nil {
			rows.Close()
			return err
		}
		due = append(due, endpoint)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, endpoint := range due {
		if endpoint.resetDay < 1 || endpoint.resetDay > 31 {
			return errors.New("center: stored Meridian entry reset day is invalid")
		}
		location, err := time.LoadLocation(endpoint.timezone)
		if err != nil {
			return errors.New("center: stored Meridian entry timezone is invalid")
		}
		wasEnabled, err := meridianEndpointQuotaEnabledInTx(ctx, tx, endpoint.id)
		if err != nil {
			return err
		}
		if !wasEnabled {
			if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpoint.id); err != nil {
				return err
			}
		}
		next := threeXUIResetBoundary(now, endpoint.resetDay, location)
		result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET used_bytes=0,last_reset_at=?,next_reset_at=?,updated_at=?
			WHERE id=? AND status<>'retired'`, nowText, next, nowText, endpoint.id)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian entry changed before its traffic reset")
		}
		if !wasEnabled {
			if err := s.markMeridianEndpointQuotaBoundaryChanged(ctx, tx, endpoint.id, nowText); err != nil {
				return err
			}
		}
	}
	return nil
}
