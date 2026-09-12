package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
)

func (s *Store) queueLandingClientCommand(ctx context.Context, tx *sql.Tx, record landingGrantRecord, phase string) error {
	controllerID, nodeID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return err
	}
	// The grant row is the durable backlog. Keep the existing one-active-
	// command constraint instead of inserting several pending native writers.
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1) AND kind NOT IN ('3xui.controller.manage','pulse.enrollment.create')`, nodeID).Scan(&busy); err != nil {
		return err
	}
	if busy != 0 {
		return nil
	}
	task := ThreeXUIClientCommandTask{Action: "landing_grant", GrantID: record.ID, GrantRevision: record.Revision, GrantPhase: phase}
	data, _ := json.Marshal(task)
	id := fmt.Sprintf("application-command-landing-client-%s-r%d-%s", record.ID, record.Revision, phase)
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?) ON CONFLICT(id) DO NOTHING`, id, controllerID, nodeID, nodeID, clientCommandKind, data, now, now)
	if err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, id, nodeID, "application.command", int64(record.Revision), "queued", "client landing operation queued")
}

// Refill one controller slot when it next asks for work. A restart or a lost
// response cannot discard the remaining grant phases, which live in SQLite.
func (s *Store) queueNextLandingClientCommand(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var id, phase string
	err := tx.QueryRowContext(ctx, `SELECT g.id,CASE g.status WHEN 'preparing' THEN 'prepare' WHEN 'activating' THEN 'activate' ELSE 'retire' END
		FROM landing_client_grants g JOIN three_x_ui_client_accounts p ON p.id=g.parent_id JOIN applications app ON app.id=p.controller_id
		LEFT JOIN landing_proxy_states routes ON routes.application_id=g.application_id
		WHERE app.node_id=? AND p.pending_command_id='' AND (
		 g.status IN ('preparing','activating') OR
		 (g.status='revoking' AND g.route_revision>0 AND routes.applied_revision>=g.route_revision AND routes.status='ready'))
		AND NOT EXISTS(SELECT 1 FROM application_commands command WHERE command.agent_id=? AND (command.state IN ('pending','running') OR command.reconciliation_required=1) AND command.kind NOT IN ('3xui.controller.manage','pulse.enrollment.create'))
		ORDER BY CASE g.status WHEN 'revoking' THEN 0 WHEN 'preparing' THEN 1 ELSE 2 END,g.updated_at,g.id LIMIT 1`, nodeID, nodeID).Scan(&id, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	record, err := readLandingGrant(ctx, tx, id)
	if err != nil {
		return err
	}
	return s.queueLandingClientCommand(ctx, tx, record, phase)
}

func (s *Store) hydrateLandingClientCommand(ctx context.Context, tx *sql.Tx, command *ThreeXUIClientCommandTask) error {
	record, err := readLandingGrant(ctx, tx, command.GrantID)
	if err != nil {
		return err
	}
	if record.Revision != command.GrantRevision || command.GrantPhase != "prepare" && command.GrantPhase != "activate" && command.GrantPhase != "retire" {
		return errors.New("center: stale landing client command")
	}
	var controllerID, mode string
	if err := tx.QueryRowContext(ctx, `SELECT controller_id,mode FROM three_x_ui_client_accounts WHERE id=?`, record.ParentID).Scan(&controllerID, &mode); err != nil {
		return err
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controllerID)
	if err != nil {
		return err
	}
	var selected *ThreeXUIClientInbound
	for i := range inbounds {
		if inbounds[i].ServiceID == record.ServiceID {
			selected = &inbounds[i]
		}
	}
	if command.GrantPhase != "retire" && (selected == nil || selected.ApplicationID != record.ApplicationID || selected.InboundTag != record.Grant.InboundTag) {
		return errors.New("center: landing entry ownership changed")
	}
	if command.GrantPhase != "retire" {
		if err := s.checkClientLandingIdentity(ctx, tx, record); err != nil {
			return err
		}
	}
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id=?`, record.CredentialSecretID).Scan(&sealed); err != nil {
		return err
	}
	credential, err := secret.Open(s.key, sealed, []byte("landing-credential:"+record.ID))
	if err != nil || landing.Identity(string(credential)) != record.Grant.FixedIdentity {
		return errors.New("center: landing credential is unavailable")
	}
	var landingName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM agents WHERE id=?`, record.LandingNodeID).Scan(&landingName); err != nil {
		return err
	}
	command.Landing = &landing.ControllerTask{Grant: record.Grant.Published(landing.PublishingMode(mode)), Revision: record.Revision, Phase: command.GrantPhase, ControllerID: controllerID, FixedUUID: string(credential), LandingName: landingName, Mode: landing.PublishingMode(mode)}
	if selected != nil {
		command.Landing.InboundID = selected.ID
		command.Landing.ConnectHostname = selected.ConnectHostname
		command.Landing.EntryName = selected.NodeName
	}
	return nil
}

func (s *Store) checkClientLandingIdentity(ctx context.Context, tx *sql.Tx, record landingGrantRecord) error {
	var sourceJSON, peerJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT c.peer_json FROM landing_client_capabilities c JOIN applications app ON app.node_id=c.node_id JOIN agents a ON a.id=c.node_id WHERE app.id=? AND c.generation=? AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed'`, record.ApplicationID, landing.ClientRuntimeGeneration).Scan(&sourceJSON); err != nil {
		return errors.New("center: entry identity unavailable")
	}
	var source, peer landing.PeerIdentity
	if json.Unmarshal(sourceJSON, &source) != nil || source != record.Source {
		return errors.New("center: entry private identity changed")
	}
	if err := tx.QueryRowContext(ctx, `SELECT s.peer_json FROM landing_server_states s JOIN agents a ON a.id=s.node_id WHERE s.node_id=? AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed'`, record.LandingNodeID).Scan(&peerJSON); err != nil || json.Unmarshal(peerJSON, &peer) != nil || peer != record.Grant.Peer {
		return errors.New("center: landing private identity changed")
	}
	return nil
}

func (s *Store) completeLandingClientCommand(ctx context.Context, tx *sql.Tx, taskID, nodeID string, input ThreeXUIClientCommandTask, succeeded bool, raw json.RawMessage) error {
	record, err := readLandingGrant(ctx, tx, input.GrantID)
	if err != nil {
		return err
	}
	if record.Revision != input.GrantRevision {
		return errors.New("center: stale landing client completion")
	}
	var envelope ApplicationTaskResult
	var result *landing.ControllerResult
	if succeeded && json.Unmarshal(raw, &envelope) == nil && envelope.ClientCommand != nil {
		result = envelope.ClientCommand.Landing
	}
	if result == nil || result.GrantID != record.ID || result.Revision != record.Revision || result.Phase != input.GrantPhase {
		succeeded = false
	}
	status, message := "succeeded", ""
	if !succeeded {
		status, message = "failed", "Landing configuration failed; retry the operation."
		if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='failed',last_error=?,updated_at=? WHERE id=?`, message, s.now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
			return err
		}
	} else {
		switch input.GrantPhase {
		case "prepare":
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='prepared' WHERE id=?`, record.ID); err != nil {
				return err
			}
			var waiting int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM landing_client_grants WHERE application_id=? AND status='preparing'`, record.ApplicationID).Scan(&waiting); err != nil {
				return err
			}
			if waiting == 0 {
				if err := s.queueClientLandingRoutes(ctx, tx, record.ApplicationID); err != nil {
					return err
				}
			}
		case "activate":
			if result.BaseLink == "" || result.SubscriptionToken == "" {
				return errors.New("center: subscription material was not confirmed")
			}
			data, _ := json.Marshal(result)
			secretID, err := s.putSecret(ctx, tx, data, "landing-material:"+record.ID)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='ready',applied_revision=desired_revision,material_secret_id=?,last_error='',updated_at=? WHERE id=?`, secretID, s.now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
				return err
			}
			if record.MaterialSecretID.Valid {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, record.MaterialSecretID.String); err != nil {
					return err
				}
			}
			if err := s.reconcileLandingSubscriptionOrigin(ctx, tx, nodeID, s.now().UTC()); err != nil {
				return err
			}
		case "retire":
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='revoked',applied_revision=desired_revision,material_secret_id=NULL,last_error='',updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
				return err
			}
			if err := s.refreshClientLandingSources(ctx, tx, record.LandingNodeID); err != nil {
				return err
			}
		default:
			return errors.New("center: invalid landing completion phase")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json='{}',error=?,lease_expires_at='',updated_at=? WHERE id=? AND state='running'`, status, message, s.now().UTC().Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, nodeID, "application.command", int64(record.Revision), status, message); err != nil {
		return err
	}
	if succeeded && input.GrantPhase == "retire" {
		// The native child is confirmed disabled/detached; its accounting
		// tombstone remains in the Agent journal. Release active topology FKs
		// and rotate the identity if this combination is authorized again.
		if _, err := tx.ExecContext(ctx, `DELETE FROM landing_client_grants WHERE id=?`, record.ID); err != nil {
			return err
		}
		for _, id := range []string{record.CredentialSecretID, record.MaterialSecretID.String} {
			if id != "" {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, id); err != nil {
					return err
				}
			}
		}
		if err := s.recordTaskEvent(ctx, tx, taskID, nodeID, "application.command", int64(record.Revision), "succeeded", "Grant "+record.ID+" revoked for parent "+record.ParentID+" on entry "+record.ApplicationID+" and landing "+record.LandingNodeID+"."); err != nil {
			return err
		}
		if err := s.queueClientLandingRoutes(ctx, tx, record.ApplicationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) queueClientLandingRoutes(ctx context.Context, tx *sql.Tx, applicationID string) error {
	var nodeID string
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id=?`, applicationID).Scan(&nodeID); err != nil {
		return err
	}
	var state landing.DesiredState
	var data []byte
	var status, owner, source string
	var serverRevision uint64
	err := tx.QueryRowContext(ctx, `SELECT desired_json,status,landing_node_id,source_address,server_revision FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&data, &status, &owner, &source, &serverRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(data) > 0 && (json.Unmarshal(data, &state) != nil || state.Validate() != nil) {
		return errors.New("center: invalid existing landing state")
	}
	if status == "applying" {
		return errors.New("center: landing route operation is still applying")
	}
	state.NodeID, state.Revision = nodeID, state.Revision+1
	state.Clients = &landing.ClientPlan{ApplicationID: applicationID, AllowSessionReset: true}
	if err := loadLandingClientBlocks(ctx, tx, applicationID, state.Clients); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.id FROM landing_client_grants g WHERE g.application_id=? AND g.status<>'revoked' ORDER BY g.id`, applicationID)
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
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return err
		}
		blocked := false
		for _, block := range state.Clients.BlockedUsers {
			if block.ParentID == record.ParentID {
				blocked = true
			}
		}
		if record.Enabled && !blocked {
			if err := s.checkClientLandingIdentity(ctx, tx, record); err != nil {
				return err
			}
		}
		var mode string
		if err := tx.QueryRowContext(ctx, `SELECT mode FROM three_x_ui_client_accounts WHERE id=?`, record.ParentID).Scan(&mode); err != nil {
			return err
		}
		grant := record.Grant.Published(landing.PublishingMode(mode))
		grant.Enabled = grant.Enabled && !blocked
		if grant.Enabled {
			if state.Clients.Source.ID != "" && state.Clients.Source != record.Source {
				return errors.New("center: landing grants belong to different entry identities")
			}
			state.Clients.Source = record.Source
		}
		state.Clients.Grants = append(state.Clients.Grants, grant)
		if owner == "" {
			owner, source = record.LandingNodeID, record.Source.Address
		}
		if err := s.refreshClientLandingSources(ctx, tx, record.LandingNodeID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status=CASE WHEN json_extract(grant_json,'$.enabled')=1 THEN 'configuring' ELSE 'revoking' END,route_revision=?,updated_at=? WHERE id=? AND status<>'ready'`, state.Revision, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
	}
	if owner == "" && len(state.Clients.BlockedUsers) > 0 {
		// A parent-only deny has no landing dependency. The legacy owner
		// column is not used by the client plan or its task prerequisites.
		owner = nodeID
	}
	if len(state.Clients.Grants)+len(state.Clients.BlockedUsers) == 0 {
		state.Clients = nil
	}
	if err := state.Validate(); err != nil {
		return err
	}
	data, _ = json.Marshal(state)
	if _, err := tx.ExecContext(ctx, `INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at) VALUES(?,?,?,?,?,?,?,'pending',?) ON CONFLICT(node_id) DO UPDATE SET desired_revision=excluded.desired_revision,desired_json=excluded.desired_json,status='pending',last_error='',lease_expires_at='',updated_at=excluded.updated_at`, nodeID, applicationID, owner, serverRevision, source, state.Revision, data, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, landingProxyTaskID(nodeID, int64(state.Revision)), nodeID, "landing.proxy.apply", int64(state.Revision), "queued", "client landing routes queued")
}

func (s *Store) completeClientLandingRoutes(ctx context.Context, tx *sql.Tx, nodeID string, revision uint64, succeeded bool) error {
	if !succeeded {
		if err := s.failLandingParentFence(ctx, tx, nodeID); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.id FROM landing_client_grants g JOIN applications a ON a.id=g.application_id WHERE a.node_id=? AND g.route_revision=? AND g.status IN ('configuring','revoking') ORDER BY g.id`, nodeID, revision)
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
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return err
		}
		if !succeeded {
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='failed',last_error='Landing configuration failed; retry the operation.' WHERE id=?`, id); err != nil {
				return err
			}
			continue
		}
		var pending string
		if err := tx.QueryRowContext(ctx, `SELECT pending_command_id FROM three_x_ui_client_accounts WHERE id=?`, record.ParentID).Scan(&pending); err != nil {
			return err
		}
		if pending != "" {
			continue
		}
		var blocks int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM landing_client_blocks WHERE parent_id=?`, record.ParentID).Scan(&blocks); err != nil {
			return err
		}
		if blocks > 0 && record.Enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='paused',applied_revision=desired_revision,material_secret_id=NULL,last_error='' WHERE id=?`, id); err != nil {
				return err
			}
			if record.MaterialSecretID.Valid {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, record.MaterialSecretID.String); err != nil {
					return err
				}
			}
			continue
		}
		phase := "activate"
		if !record.Enabled {
			phase = "retire"
		}
		if record.Enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET status='activating' WHERE id=?`, id); err != nil {
				return err
			}
		}
		if err := s.queueLandingClientCommand(ctx, tx, record, phase); err != nil {
			return err
		}
	}
	return nil
}
