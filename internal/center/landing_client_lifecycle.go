package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) prepareLandingParentMutation(ctx context.Context, tx *sql.Tx, commandID string, input ThreeXUIClientCommandInput, task *ThreeXUIClientCommandTask) error {
	if !slices.Contains([]string{"update", "set_enabled", "reset_traffic", "delete"}, input.Action) {
		return nil
	}
	var parentID, pending string
	var metadata []byte
	err := tx.QueryRowContext(ctx, `SELECT id,metadata_json,pending_command_id FROM three_x_ui_client_accounts WHERE controller_id=? AND email=? AND (managed_quota=1 OR EXISTS(SELECT 1 FROM landing_client_grants WHERE parent_id=three_x_ui_client_accounts.id))`, input.ApplicationID, input.Email).Scan(&parentID, &metadata, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if pending != "" || !input.ConfirmSessionReset {
		return errors.New("center: confirm a brief disconnection on this client's entry instances before changing its plan")
	}
	client, err := landingAccountMetadata(metadata, parentID)
	if err != nil {
		return err
	}
	if input.Action == "update" {
		client.InboundIDs = normalizedThreeXUIInboundIDs(append(slices.Clone(client.InboundIDs), input.InboundIDs...))
	}
	apps := []string{}
	for _, inbound := range task.Inbounds {
		if !slices.Contains(client.InboundIDs, inbound.ID) {
			continue
		}
		var capable int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM landing_client_capabilities c JOIN agents a ON a.id=c.node_id WHERE c.node_id=? AND c.generation=? AND a.status='active' AND a.credential_revoked_at=''`, inbound.NodeID, landing.ClientRuntimeGeneration).Scan(&capable); err != nil {
			return err
		}
		if capable != 1 {
			return errors.New("center: reconnect and update this client's entry Agents before changing its shared account")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO landing_client_blocks(parent_id,application_id,inbound_tag,user_name,identity) VALUES(?,?,?,?,?) ON CONFLICT(parent_id,application_id,inbound_tag,user_name) DO UPDATE SET identity=excluded.identity`, parentID, inbound.ApplicationID, inbound.InboundTag, input.Email, parentID); err != nil {
			return err
		}
		if !slices.Contains(apps, inbound.ApplicationID) {
			apps = append(apps, inbound.ApplicationID)
		}
	}
	if len(apps) == 0 {
		return errors.New("center: shared account entry ownership is unavailable")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO landing_client_blocks(parent_id,application_id,inbound_tag,user_name,identity) SELECT parent_id,application_id,json_extract(grant_json,'$.inboundTag'),json_extract(grant_json,'$.fixedUser'),json_extract(grant_json,'$.fixedIdentity') FROM landing_client_grants WHERE parent_id=? AND status<>'revoked' ON CONFLICT(parent_id,application_id,inbound_tag,user_name) DO UPDATE SET identity=excluded.identity`, parentID); err != nil {
		return err
	}
	// The confirmation covers the parent's displayed entry instances. Do not
	// silently restart an old child-only entry, or leave a command waiting for
	// a fence that was never queued. Its grant must be revoked explicitly first.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT application_id FROM landing_client_blocks WHERE parent_id=?`, parentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var app string
		if err := rows.Scan(&app); err != nil {
			rows.Close()
			return err
		}
		if !slices.Contains(apps, app) {
			rows.Close()
			return errors.New("center: revoke landing grants on this client's previous entries before changing its plan")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_client_accounts SET pending_command_id=? WHERE id=?`, commandID, parentID); err != nil {
		return err
	}
	for _, app := range apps {
		if err := s.queueClientLandingRoutes(ctx, tx, app); err != nil {
			return err
		}
	}
	task.ManagedParentID = parentID
	return nil
}

func loadLandingClientBlocks(ctx context.Context, tx *sql.Tx, appID string, plan *landing.ClientPlan) error {
	rows, err := tx.QueryContext(ctx, `SELECT parent_id,inbound_tag,user_name,identity FROM landing_client_blocks WHERE application_id=? ORDER BY parent_id,inbound_tag`, appID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var block landing.ClientBlock
		if err := rows.Scan(&block.ParentID, &block.InboundTag, &block.User, &block.Identity); err != nil {
			return err
		}
		plan.BlockedUsers = append(plan.BlockedUsers, block)
	}
	return rows.Err()
}

func (s *Store) landingParentMutationReady(ctx context.Context, tx *sql.Tx, commandID, parentID string) (bool, error) {
	var pending string
	if err := tx.QueryRowContext(ctx, `SELECT pending_command_id FROM three_x_ui_client_accounts WHERE id=?`, parentID).Scan(&pending); err != nil {
		return false, err
	}
	if pending != commandID {
		return false, errors.New("center: parent operation changed")
	}
	var total, incomplete int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN p.node_id IS NULL OR p.applied_revision<>p.desired_revision OR p.status<>'ready' THEN 1 ELSE 0 END),0) FROM landing_client_blocks b LEFT JOIN landing_proxy_states p ON p.application_id=b.application_id WHERE b.parent_id=?`, parentID).Scan(&total, &incomplete); err != nil {
		return false, err
	}
	return total > 0 && incomplete == 0, nil
}

func (s *Store) finishLandingParentMutation(ctx context.Context, tx *sql.Tx, input ThreeXUIClientCommandTask, result ThreeXUIClientCommandResult) error {
	if input.ManagedParentID == "" {
		return nil
	}
	var client *ThreeXUIClientView
	for i := range result.Clients {
		if result.Clients[i].ID == input.ManagedParentID {
			client = &result.Clients[i]
		}
	}
	if client == nil && input.Action != "delete" {
		return errors.New("center: shared parent result was not observed")
	}
	rows, err := tx.QueryContext(ctx, `SELECT application_id FROM landing_client_blocks WHERE parent_id=? ORDER BY application_id`, input.ManagedParentID)
	if err != nil {
		return err
	}
	apps := []string{}
	for rows.Next() {
		var app string
		if err := rows.Scan(&app); err != nil {
			rows.Close()
			return err
		}
		if !slices.Contains(apps, app) {
			apps = append(apps, app)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if input.Action == "delete" {
		return s.finishDeletedLandingParent(ctx, tx, input.ManagedParentID, apps)
	}
	rows, err = tx.QueryContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND status<>'revoked' ORDER BY id`, input.ManagedParentID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_client_accounts SET pending_command_id='',revision=revision+1 WHERE id=?`, input.ManagedParentID); err != nil {
		return err
	}
	if client != nil && client.Enabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM landing_client_blocks WHERE parent_id=?`, input.ManagedParentID); err != nil {
			return err
		}
	}
	preparing := map[string]bool{}
	for _, id := range ids {
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return err
		}
		record.Revision++
		if client != nil {
			record.Grant.BaseUser = client.Email
		}
		allowed := client != nil && client.Enabled
		if input.Action == "delete" {
			record.Grant.Enabled = false
		}
		if input.Action == "update" {
			for _, inbound := range input.Inbounds {
				if inbound.ServiceID == record.ServiceID && !slices.Contains(input.InboundIDs, inbound.ID) {
					record.Grant.Enabled = false
				}
			}
		}
		data, _ := json.Marshal(record.Grant)
		status := "prepared"
		if allowed && record.Grant.Enabled {
			status = "preparing"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET grant_json=?,desired_revision=?,status=?,last_error='',updated_at=? WHERE id=?`, data, record.Revision, status, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if status == "preparing" {
			if err := s.queueLandingClientCommand(ctx, tx, record, "prepare"); err != nil {
				return err
			}
			preparing[record.ApplicationID] = true
		}
	}
	for _, app := range apps {
		if !preparing[app] {
			if err := s.queueClientLandingRoutes(ctx, tx, app); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) failLandingParentFence(ctx context.Context, tx *sql.Tx, nodeID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT p.pending_command_id FROM three_x_ui_client_accounts p JOIN landing_client_blocks b ON b.parent_id=p.id JOIN applications a ON a.id=b.application_id WHERE a.node_id=? AND p.pending_command_id<>''`, nodeID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='failed',reconciliation_required=1,error='Entry disconnection was not confirmed; retry reconciliation.',updated_at=? WHERE id=? AND state='pending'`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, id, nodeID, "application.command", 1, "failed", "Entry disconnection was not confirmed."); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) retryLandingParentFence(ctx context.Context, tx *sql.Tx, commandID string) error {
	var parentID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(input_json,'$.managedParentId'),'') FROM application_commands WHERE id=?`, commandID).Scan(&parentID); err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	// Reuse the same desired revision/checkpoint. A fresh revision could
	// incorrectly replace a partially applied cutover after a lost response.
	_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET status='pending',lease_expires_at='',last_error='',updated_at=? WHERE status='failed' AND application_id IN (SELECT application_id FROM landing_client_blocks WHERE parent_id=?)`, s.now().UTC().Format(time.RFC3339Nano), parentID)
	return err
}
