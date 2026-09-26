package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type LandingEgressInput struct {
	Revision uint64 `json:"revision"`
	EgressIP string `json:"egressIp"`
}

// Change only this server's egress binding; source authorization and account
// routes remain owned by the existing grant machinery.
func (s *Store) SetLandingEgress(ctx context.Context, nodeID string, input LandingEgressInput) error {
	if input.EgressIP != "" && !landing.ValidEgressIP(input.EgressIP) {
		return errors.New("center: use a canonical local IPv4 or global IPv6 address")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return err
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if !slices.Contains(selection.NodeIDs, nodeID) {
		return errors.New("center: landing server is not selected")
	}
	var raw []byte
	var revision uint64
	var status, seen string
	var supported, blocked bool
	err = tx.QueryRowContext(ctx, `SELECT state.desired_json,state.desired_revision,state.status,agent.last_seen_at,
 COALESCE(json_extract(agent.capabilities_json,'$.landingEgressIP'),0)=1 AND agent.status='active' AND agent.credential_revoked_at='',
 EXISTS(SELECT 1 FROM task_executions WHERE agent_id=agent.id AND disposition='' AND state<>'succeeded')
 FROM landing_server_states state JOIN agents agent ON agent.id=state.node_id WHERE state.node_id=?`, nodeID).Scan(&raw, &revision, &status, &seen, &supported, &blocked)
	if err != nil {
		return err
	}
	lastSeen, _ := time.Parse(time.RFC3339Nano, seen)
	if !supported || s.now().Sub(lastSeen) > 2*time.Minute {
		return errors.New("center: update and reconnect the landing Agent before selecting an egress IP")
	}
	if blocked || status == "pending" || status == "applying" {
		return errExecutionBlocked
	}
	if revision != input.Revision {
		return errors.New("center: landing configuration changed; refresh and retry")
	}
	var state landing.ServerState
	if json.Unmarshal(raw, &state) != nil || state.Validate() != nil || state.Plan == nil || state.NodeID != nodeID {
		return errLandingSourceReconciliation
	}
	if state.Plan.EgressIP == input.EgressIP {
		return nil
	}
	// Every current entry must understand dual-stack health evidence. Old Agents
	// must never silently interpret the new exit configuration as default IPv4.
	var outdated int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants grant
 JOIN meridian_endpoints endpoint ON endpoint.id=grant.endpoint_id
 JOIN applications application ON application.id=endpoint.application_id
 JOIN agents agent ON agent.id=application.node_id
 WHERE grant.egress_node_id=? AND grant.status<>'revoked'
 AND COALESCE(json_extract(agent.capabilities_json,'$.landingEgressIP'),0)<>1`, nodeID).Scan(&outdated); err != nil {
		return err
	}
	if outdated != 0 {
		return errors.New("center: update connected entry Agents before changing the landing egress IP")
	}
	state.Plan.EgressIP = input.EgressIP
	if err := state.Plan.Validate(); err != nil {
		return err
	}
	if err := s.queueLandingServer(ctx, tx, nodeID, state.Plan); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) handleLandingEgress(writer http.ResponseWriter, request *http.Request) {
	var input LandingEgressInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetLandingEgress(request.Context(), request.PathValue("id"), input); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	s.handleLanding(writer, request)
}
