package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

var errNodeExitControllerBlocked = errors.New("center: subscription controller has unresolved tasks; exit configuration was not saved")

type NodeExitPolicy struct {
	ApplicationID      string            `json:"applicationId"`
	OwnExit            bool              `json:"ownExit"`
	LandingNodeIDs     []string          `json:"landingNodeIds"`
	LandingRegionCodes map[string]string `json:"landingRegionCodes,omitempty"`
	Revision           uint64            `json:"revision"`
	Status             string            `json:"status,omitempty"`
	RequiresOwnExit    bool              `json:"requiresOwnExit,omitempty"`
}

type NodeExitInput struct {
	OwnExit             bool              `json:"ownExit"`
	LandingNodeIDs      []string          `json:"landingNodeIds"`
	LandingRegionCodes  map[string]string `json:"landingRegionCodes"`
	Revision            uint64            `json:"revision"`
	ConfirmSessionReset bool              `json:"confirmSessionReset"`
}

func readNodeExitPolicy(ctx context.Context, tx *sql.Tx, applicationID string) (NodeExitPolicy, error) {
	p := NodeExitPolicy{ApplicationID: applicationID, OwnExit: true, LandingNodeIDs: []string{}}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, "node-exits:"+applicationID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if json.Unmarshal([]byte(raw), &p) != nil || p.ApplicationID != applicationID || p.Revision == 0 {
		return p, errors.New("center: invalid node exit policy")
	}
	return p, nil
}

func (s *Store) ConfigureNodeExits(ctx context.Context, applicationID string, input NodeExitInput) error {
	if !input.ConfirmSessionReset || (!input.OwnExit && len(input.LandingNodeIDs) == 0) || len(input.LandingNodeIDs) > landing.MaxServers {
		return errors.New("center: select at least one exit and confirm the change")
	}
	slices.Sort(input.LandingNodeIDs)
	for i, id := range input.LandingNodeIDs {
		if id == "" || i > 0 && id == input.LandingNodeIDs[i-1] {
			return errors.New("center: invalid exit selection")
		}
	}
	for id, value := range input.LandingRegionCodes {
		code, ok := regionCode(value)
		if !ok || !slices.Contains(input.LandingNodeIDs, id) {
			return errors.New("center: invalid landing region")
		}
		input.LandingRegionCodes[id] = code
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	controller, controllerNode, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return err
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controller)
	if err != nil {
		return err
	}
	var entry *ThreeXUIClientInbound
	for i := range inbounds {
		if inbounds[i].ApplicationID == applicationID && !inbounds[i].VLESSDisabled {
			entry = &inbounds[i]
			break
		}
	}
	if entry == nil {
		return errors.New("center: managed VLESS entry is unavailable")
	}
	if !input.OwnExit && entry.HY2InboundID != 0 {
		return errors.New("center: keep the own exit while HY2 is enabled; landing combinations support VLESS only")
	}
	var controllerBlocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, controllerNode).Scan(&controllerBlocked); err != nil {
		return err
	}
	if controllerBlocked {
		return errNodeExitControllerBlocked
	}
	for _, nodeID := range append([]string{entry.NodeID}, input.LandingNodeIDs...) {
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, nodeID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return errExecutionBlocked
		}
	}
	old, err := readNodeExitPolicy(ctx, tx, applicationID)
	if err != nil {
		return err
	}
	if old.Revision != input.Revision {
		return errors.New("center: exit selection changed; refresh and retry")
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_commands WHERE agent_id IN (?,?) AND (state IN ('pending','running') OR reconciliation_required=1)) OR EXISTS(SELECT 1 FROM landing_client_grants WHERE application_id=? AND status NOT IN ('ready','revoked','failed','paused'))`, controllerNode, entry.NodeID, applicationID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errors.New("center: wait for the current node operation to finish")
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range input.LandingNodeIDs {
		if id == entry.NodeID || !slices.Contains(selection.NodeIDs, id) {
			return errors.New("center: choose a managed landing server")
		}
		var ready bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states s JOIN agents a ON a.id=s.node_id WHERE s.node_id=? AND s.status='ready' AND s.desired_revision=s.applied_revision AND a.status='active' AND a.credential_revoked_at='' AND a.last_seen_at>?)`, id, s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano)).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return errors.New("center: landing server is not ready")
		}
	}
	// A node-wide forced exit must not redirect the original subscription node.
	var forcedRevision uint64
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM landing_proxy_states WHERE application_id=? AND json_extract(desired_json,'$.proxy') IS NOT NULL`, applicationID).Scan(&forcedRevision); err == nil {
		if err := s.configureLandingProxy(ctx, tx, applicationID, LandingProxyInput{Revision: forcedRevision}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	p := NodeExitPolicy{ApplicationID: applicationID, OwnExit: input.OwnExit, LandingNodeIDs: input.LandingNodeIDs, LandingRegionCodes: input.LandingRegionCodes, Revision: old.Revision + 1}
	if p.LandingNodeIDs == nil {
		p.LandingNodeIDs = []string{}
	}
	if p.LandingRegionCodes == nil {
		p.LandingRegionCodes = map[string]string{}
	}
	raw, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "node-exits:"+applicationID, string(raw)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM landing_client_grants WHERE application_id=? AND status<>'revoked'`, applicationID)
	if err != nil {
		return err
	}
	var ids []string
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
		r, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return err
		}
		if !slices.Contains(p.LandingNodeIDs, r.LandingNodeID) {
			if _, err := s.revokeClientLandingTx(ctx, tx, LandingClientGrantInput{ParentID: r.ParentID, ServiceID: r.ServiceID, LandingNodeID: r.LandingNodeID, Revision: r.Revision}); err != nil {
				return err
			}
		}
	}
	if err := s.syncNodeExitClients(ctx, tx, controller, inbounds, applicationID, false); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, "node-exits-error:"+applicationID); err != nil {
		return err
	}
	return tx.Commit()
}

// Called after observed client inventory changes too: inherit topology only
// for clients already attached to this entry, never grant access to new entries.
func (s *Store) syncNodeExitClients(ctx context.Context, tx *sql.Tx, controller string, inbounds []ThreeXUIClientInbound, onlyApplication string, automatic bool) error {
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	rows, err := tx.QueryContext(ctx, `SELECT metadata_json FROM three_x_ui_client_accounts WHERE controller_id=? AND email=json_extract(metadata_json,'$.email') ORDER BY id`, controller)
	if err != nil {
		return err
	}
	var clients []ThreeXUIClientView
	for rows.Next() {
		var raw []byte
		var c ThreeXUIClientView
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(raw, &c) != nil {
			rows.Close()
			return errors.New("center: invalid client inventory")
		}
		clients = append(clients, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, entry := range inbounds {
		if onlyApplication != "" && entry.ApplicationID != onlyApplication {
			continue
		}
		p, err := readNodeExitPolicy(ctx, tx, entry.ApplicationID)
		if err != nil {
			return err
		}
		if p.Revision == 0 {
			continue
		}
		for _, c := range clients {
			if !c.Enabled || !slices.Contains(c.InboundIDs, entry.ID) || c.ExpiryTime > 0 && c.ExpiryTime <= s.now().UnixMilli() || c.TotalBytes > 0 && c.UsedBytes >= c.TotalBytes {
				continue
			}
			for _, target := range p.LandingNodeIDs {
				var id string
				var revision uint64
				err := tx.QueryRowContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND application_id=? AND landing_node_id=?`, c.ID, entry.ApplicationID, target).Scan(&id)
				if err == nil {
					g, err := readLandingGrant(ctx, tx, id)
					if err != nil {
						return err
					}
					revision = g.Revision
					if automatic && (g.Status != "ready" && g.Status != "revoked" || g.Enabled && g.Grant.HideBase == !p.OwnExit && g.Status == "ready") {
						continue
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if _, err := s.configureClientLanding(ctx, tx, LandingClientGrantInput{ParentID: c.ID, ServiceID: entry.ServiceID, LandingNodeID: target, Revision: revision, Mode: landing.FixedMode, Enabled: true, ConfirmSessionReset: true}, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Server) handleConfigureNodeExits(w http.ResponseWriter, r *http.Request) {
	var input NodeExitInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.ConfigureNodeExits(r.Context(), r.PathValue("id"), input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.handleLanding(w, r)
}
