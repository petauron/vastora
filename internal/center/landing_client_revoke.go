package center

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) revokeClientLanding(ctx context.Context, input LandingClientGrantInput) (LandingClientGrantView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingClientGrantView{}, err
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND service_id=? AND landing_node_id=?`, input.ParentID, input.ServiceID, input.LandingNodeID).Scan(&id); err != nil {
		return LandingClientGrantView{}, errors.New("center: landing grant was not found")
	}
	record, err := readLandingGrant(ctx, tx, id)
	if err != nil {
		return LandingClientGrantView{}, err
	}
	if record.Revision != input.Revision {
		return LandingClientGrantView{}, errors.New("center: grant changed; refresh and retry")
	}
	if record.Status == "revoked" {
		return record.LandingClientGrantView, nil
	}
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE json_extract(input_json,'$.grantId')=? AND state='running'`, id).Scan(&running); err != nil {
		return LandingClientGrantView{}, err
	}
	if running > 0 {
		return LandingClientGrantView{}, errors.New("center: wait for the current landing operation to finish")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='failed',error='Superseded by revocation.',updated_at=? WHERE json_extract(input_json,'$.grantId')=? AND state='pending'`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return LandingClientGrantView{}, err
	}
	record.Grant.Enabled = false
	record.Enabled = false
	record.Revision++
	record.Status = "revoking"
	data, _ := json.Marshal(record.Grant)
	if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET grant_json=?,desired_revision=?,status='revoking',last_error='',updated_at=? WHERE id=?`, data, record.Revision, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return LandingClientGrantView{}, err
	}
	if err := s.queueClientLandingRoutes(ctx, tx, record.ApplicationID); err != nil {
		return LandingClientGrantView{}, err
	}
	if err := tx.Commit(); err != nil {
		return LandingClientGrantView{}, err
	}
	return record.LandingClientGrantView, nil
}
