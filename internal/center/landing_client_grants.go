package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/petauron/vastora/internal/landing"
)

type LandingClientGrantInput struct {
	ParentID            string                 `json:"parentId"`
	ServiceID           string                 `json:"serviceId"`
	LandingNodeID       string                 `json:"landingNodeId"`
	Mode                landing.PublishingMode `json:"mode"`
	Enabled             bool                   `json:"enabled"`
	Revision            uint64                 `json:"revision"`
	ConfirmSessionReset bool                   `json:"confirmSessionReset"`
}

type LandingClientGrantView struct {
	ID              string                 `json:"id"`
	ParentID        string                 `json:"parentId"`
	ApplicationID   string                 `json:"applicationId"`
	ServiceID       string                 `json:"serviceId"`
	LandingNodeID   string                 `json:"landingNodeId"`
	Mode            landing.PublishingMode `json:"mode"`
	Enabled         bool                   `json:"enabled"`
	Revision        uint64                 `json:"revision"`
	AppliedRevision uint64                 `json:"appliedRevision"`
	Status          string                 `json:"status"`
	Error           string                 `json:"error,omitempty"`
}

type landingGrantRecord struct {
	LandingClientGrantView
	Grant              landing.ClientGrant
	Source             landing.PeerIdentity
	CredentialSecretID string
	MaterialSecretID   sql.NullString
	RouteRevision      uint64
}

func readLandingGrant(ctx context.Context, tx *sql.Tx, id string) (landingGrantRecord, error) {
	var r landingGrantRecord
	var grant, source []byte
	err := tx.QueryRowContext(ctx, `SELECT id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,material_secret_id,desired_revision,applied_revision,route_revision,status,last_error FROM landing_client_grants WHERE id=?`, id).Scan(&r.ID, &r.ParentID, &r.ApplicationID, &r.ServiceID, &r.LandingNodeID, &source, &grant, &r.CredentialSecretID, &r.MaterialSecretID, &r.Revision, &r.AppliedRevision, &r.RouteRevision, &r.Status, &r.Error)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(grant, &r.Grant) != nil || r.Grant.Validate() != nil || json.Unmarshal(source, &r.Source) != nil || r.Grant.ID != id || r.Grant.ParentID != r.ParentID {
		return r, errors.New("center: invalid landing grant record")
	}
	r.Enabled, r.Mode = r.Grant.Enabled, r.Grant.Mode
	return r, nil
}

func (s *Store) LandingClientGrants(ctx context.Context, parentID string) ([]LandingClientGrantView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? ORDER BY id`, parentID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []LandingClientGrantView{}
	for _, id := range ids {
		r, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, r.LandingClientGrantView)
	}
	return result, nil
}

func (s *Store) ConfigureClientLanding(ctx context.Context, input LandingClientGrantInput) (LandingClientGrantView, error) {
	if input.ParentID == "" || input.ServiceID == "" || input.LandingNodeID == "" || !input.Mode.Valid() || !input.ConfirmSessionReset {
		return LandingClientGrantView{}, errors.New("center: choose a client, entry and landing; confirm the selected proxy instance may briefly disconnect")
	}
	if !input.Enabled {
		return s.revokeClientLanding(ctx, input)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingClientGrantView{}, err
	}
	defer tx.Rollback()
	var controllerID, email string
	var metadata []byte
	if err := tx.QueryRowContext(ctx, `SELECT controller_id,email,metadata_json FROM three_x_ui_client_accounts WHERE id=?`, input.ParentID).Scan(&controllerID, &email, &metadata); err != nil {
		return LandingClientGrantView{}, errors.New("center: refresh the client list first")
	}
	controller, controllerNode, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || controller != controllerID {
		return LandingClientGrantView{}, errors.New("center: subscription controller changed")
	}
	var client ThreeXUIClientView
	if json.Unmarshal(metadata, &client) != nil || client.ID != input.ParentID {
		return LandingClientGrantView{}, errors.New("center: invalid client identity")
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controllerID)
	if err != nil {
		return LandingClientGrantView{}, err
	}
	var selected *ThreeXUIClientInbound
	for i := range inbounds {
		if inbounds[i].ServiceID == input.ServiceID {
			selected = &inbounds[i]
		}
	}
	if selected == nil || selected.VLESSDisabled || selected.InboundTag == "" || !slices.Contains(client.InboundIDs, selected.ID) || selected.NodeID == input.LandingNodeID {
		return LandingClientGrantView{}, errors.New("center: choose an available VLESS entry belonging to this client")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_client_accounts SET managed_quota=1 WHERE id=?`, input.ParentID); err != nil {
		return LandingClientGrantView{}, err
	}
	var source, peer landing.PeerIdentity
	var sourceJSON, peerJSON []byte
	for _, nodeID := range []string{selected.NodeID, controllerNode} {
		var generation int
		if err := tx.QueryRowContext(ctx, `SELECT c.generation,c.peer_json FROM landing_client_capabilities c JOIN agents a ON a.id=c.node_id WHERE c.node_id=? AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed' AND c.observed_at>?`, nodeID, s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano)).Scan(&generation, &sourceJSON); err != nil || generation != landing.ClientRuntimeGeneration {
			return LandingClientGrantView{}, errors.New("center: update the selected Agents and wait for them to reconnect")
		}
		if nodeID == selected.NodeID && json.Unmarshal(sourceJSON, &source) != nil {
			return LandingClientGrantView{}, errors.New("center: entry private identity unavailable")
		}
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil || !slices.Contains(selection.NodeIDs, input.LandingNodeID) {
		return LandingClientGrantView{}, errors.New("center: choose a managed landing server")
	}
	if err := tx.QueryRowContext(ctx, `SELECT s.peer_json FROM landing_server_states s JOIN agents a ON a.id=s.node_id WHERE s.node_id=? AND s.status='ready' AND s.desired_revision=s.applied_revision AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed'`, input.LandingNodeID).Scan(&peerJSON); err != nil || json.Unmarshal(peerJSON, &peer) != nil {
		return LandingClientGrantView{}, errors.New("center: landing server is not ready")
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id IN (?,?) AND (state IN ('pending','running') OR reconciliation_required=1)`, controllerNode, selected.NodeID).Scan(&busy); err != nil {
		return LandingClientGrantView{}, err
	}
	if busy != 0 {
		return LandingClientGrantView{}, errors.New("center: wait for the current node operation to finish")
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND application_id=? AND landing_node_id=?`, input.ParentID, selected.ApplicationID, input.LandingNodeID).Scan(&id)
	var record landingGrantRecord
	if errors.Is(err, sql.ErrNoRows) {
		if input.Revision != 0 || !input.Enabled {
			return LandingClientGrantView{}, errors.New("center: grant changed; refresh and retry")
		}
		value, err := uuid.NewRandom()
		if err != nil {
			return LandingClientGrantView{}, err
		}
		id = value.String()
		credential, err := uuid.NewRandom()
		if err != nil {
			return LandingClientGrantView{}, err
		}
		secretID, err := s.putSecret(ctx, tx, []byte(credential.String()), "landing-credential:"+id)
		if err != nil {
			return LandingClientGrantView{}, err
		}
		record = landingGrantRecord{LandingClientGrantView: LandingClientGrantView{ID: id, ParentID: input.ParentID, ApplicationID: selected.ApplicationID, ServiceID: input.ServiceID, LandingNodeID: input.LandingNodeID}, CredentialSecretID: secretID}
		record.Grant = landing.ClientGrant{ID: id, ParentID: input.ParentID, BaseUser: email, BaseIdentity: input.ParentID, FixedUser: landing.FixedUser(id), FixedIdentity: landing.Identity(credential.String()), InboundTag: selected.InboundTag, Peer: peer}
	} else if err != nil {
		return LandingClientGrantView{}, err
	} else {
		record, err = readLandingGrant(ctx, tx, id)
		if err != nil {
			return LandingClientGrantView{}, err
		}
		if record.Revision != input.Revision || record.Status != "ready" && record.Status != "revoked" && record.Status != "failed" {
			return LandingClientGrantView{}, errors.New("center: grant changed or is still applying")
		}
		if record.Grant.Peer != peer || record.Source != source || record.ServiceID != input.ServiceID || record.Grant.BaseUser != email {
			return LandingClientGrantView{}, errors.New("center: grant identity changed; revoke its previous identity first")
		}
	}
	record.Source = source
	record.Revision++
	record.Grant.Mode, record.Grant.Enabled = input.Mode, input.Enabled
	if err := record.Grant.Validate(); err != nil {
		return LandingClientGrantView{}, err
	}
	if input.Enabled && (!client.Enabled || client.ExpiryTime > 0 && client.ExpiryTime <= s.now().UnixMilli() || client.TotalBytes > 0 && client.UsedBytes >= client.TotalBytes) {
		return LandingClientGrantView{}, errors.New("center: client is disabled, expired or out of traffic")
	}
	record.Status = "preparing"
	if !input.Enabled {
		record.Status = "revoking"
	}
	grantJSON, _ := json.Marshal(record.Grant)
	sourceJSON, _ = json.Marshal(source)
	_, err = tx.ExecContext(ctx, `INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,desired_revision,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET grant_json=excluded.grant_json,desired_revision=excluded.desired_revision,status=excluded.status,last_error='',updated_at=excluded.updated_at`, id, input.ParentID, selected.ApplicationID, input.ServiceID, input.LandingNodeID, sourceJSON, grantJSON, record.CredentialSecretID, record.Revision, record.Status, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return LandingClientGrantView{}, err
	}
	if input.Enabled {
		err = s.queueLandingClientCommand(ctx, tx, record, "prepare")
	} else {
		err = s.queueClientLandingRoutes(ctx, tx, selected.ApplicationID)
	}
	if err != nil {
		return LandingClientGrantView{}, err
	}
	if err := tx.Commit(); err != nil {
		return LandingClientGrantView{}, err
	}
	record.Mode, record.Enabled = input.Mode, input.Enabled
	return record.LandingClientGrantView, nil
}

func (s *Server) handleLandingClientGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		grants, err := s.store.LandingClientGrants(r.Context(), r.URL.Query().Get("parentId"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, grants)
		return
	}
	var input LandingClientGrantInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	grant, err := s.store.ConfigureClientLanding(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, grant)
}
