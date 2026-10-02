package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type AgentReinstallLandingSource struct {
	State    string                        `json:"state"`
	Identity MeridianSourceRecoveryView    `json:"identity"`
	Landings []AgentReinstallLandingTarget `json:"landings"`
}

type AgentReinstallLandingTarget struct {
	NodeID          string `json:"nodeId"`
	Revision        int64  `json:"revision"`
	PeerFingerprint string `json:"peerFingerprint"`
}

func (s *Store) reinstallLandingIdentity(ctx context.Context, tx *sql.Tx, agentID, preparationID string) (string, MeridianSourceRecoveryView, []byte, error) {
	var view MeridianSourceRecoveryView
	if valid, err := s.validateReinstallPreparation(ctx, tx, agentID, preparationID); err != nil || !valid {
		return "", view, nil, errExecutionAuthorization
	}
	var endpointID string
	var previousJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.source_peer_json,e.desired_revision FROM agent_reinstall_app_preparations p JOIN meridian_endpoints e ON e.application_id=p.application_id JOIN deployments d ON d.id=p.deployment_id WHERE p.deployment_id=? AND d.state='succeeded' AND e.status<>'retired'`, preparationID).Scan(&endpointID, &previousJSON, &view.EndpointRevision)
	if err != nil {
		return "", view, nil, err
	}
	review, err := s.agentReinstallNetworkReview(ctx, tx, agentID)
	if err != nil {
		return "", view, nil, err
	}
	if review == nil || !review.ApprovalCurrent || review.Approval == nil || review.Approval.PrivatePeer == nil || review.Approval.Profile.HeadscaleAddress == "" || review.Approval.ControllerID == "" {
		return "", view, nil, errExecutionAuthorization
	}
	var previous landing.PeerIdentity
	current := *review.Approval.PrivatePeer
	if json.Unmarshal(previousJSON, &previous) != nil || (meridianruntime.Peer{EgressID: agentID, Identity: previous}).Validate() != nil || (meridianruntime.Peer{EgressID: agentID, Identity: current}).Validate() != nil {
		return "", view, nil, errExecutionAuthorization
	}
	if err = tx.QueryRowContext(ctx, `SELECT replacement_network_observed_at FROM agent_reinstall_operations WHERE agent_id=? AND state='review_required'`, agentID).Scan(&view.ObservedAt); err != nil {
		return "", view, nil, err
	}
	observed, err := time.Parse(time.RFC3339Nano, view.ObservedAt)
	if err != nil || observed.After(s.now().UTC()) || !observed.After(s.now().UTC().Add(-landingHealthFreshness)) {
		return "", view, nil, errExecutionAuthorization
	}
	view.PreviousFingerprint, view.CurrentFingerprint = meridianPeerFingerprint(previous), meridianPeerFingerprint(current)
	view.PreviousAddress, view.CurrentAddress = previous.Address, current.Address
	encoded, _ := json.Marshal(current)
	return endpointID, view, encoded, nil
}

func reinstallLandingTargets(ctx context.Context, tx *sql.Tx, endpointID string) ([]AgentReinstallLandingTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT g.egress_node_id,s.desired_revision,s.peer_json FROM meridian_route_grants g LEFT JOIN landing_server_states s ON s.node_id=g.egress_node_id WHERE g.endpoint_id=? AND ((g.enabled=1 AND g.status<>'revoked') OR g.status='revoking') ORDER BY g.egress_node_id`, endpointID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []AgentReinstallLandingTarget{}
	for rows.Next() {
		var target AgentReinstallLandingTarget
		var encoded []byte
		if err = rows.Scan(&target.NodeID, &target.Revision, &encoded); err != nil {
			return nil, err
		}
		var peer landing.PeerIdentity
		if json.Unmarshal(encoded, &peer) != nil || (meridianruntime.Peer{EgressID: target.NodeID, Identity: peer}).Validate() != nil {
			return nil, errExecutionAuthorization
		}
		target.PeerFingerprint = meridianPeerFingerprint(peer)
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// While recovery fences execution, neither a missing live capability nor an old
// pin can grant landing access. Only the explicit, still-current handoff can.
func (s *Store) reinstallLandingSourceAllowed(ctx context.Context, tx *sql.Tx, agentID, endpointID, landingID string, peer landing.PeerIdentity) (bool, bool, error) {
	recovering, err := agentReinstallBlocked(ctx, tx, agentID)
	if err != nil || !recovering {
		return !recovering, recovering, err
	}
	var preparationID string
	var identityJSON, targetsJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT p.deployment_id,r.identity_json,r.targets_json FROM agent_reinstall_landing_sources r JOIN agent_reinstall_app_preparations p ON p.deployment_id=r.preparation_id JOIN agent_reinstall_operations op ON op.id=p.operation_id WHERE op.agent_id=? AND op.state='review_required' AND r.endpoint_id=? AND r.phase='authorize'`, agentID, endpointID).Scan(&preparationID, &identityJSON, &targetsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, true, err
	}
	actualEndpoint, identity, _, err := s.reinstallLandingIdentity(ctx, tx, agentID, preparationID)
	var expected MeridianSourceRecoveryView
	var targets []AgentReinstallLandingTarget
	if err != nil || actualEndpoint != endpointID || json.Unmarshal(identityJSON, &expected) != nil || json.Unmarshal(targetsJSON, &targets) != nil || identity.CurrentFingerprint != expected.CurrentFingerprint || identity.PreviousFingerprint != expected.CurrentFingerprint || meridianPeerFingerprint(peer) != expected.CurrentFingerprint {
		return false, true, nil
	}
	for _, target := range targets {
		if target.NodeID != landingID {
			continue
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT peer_json FROM landing_server_states WHERE node_id=?`, landingID).Scan(&raw); err != nil {
			return false, true, err
		}
		var actual landing.PeerIdentity
		return json.Unmarshal(raw, &actual) == nil && meridianPeerFingerprint(actual) == target.PeerFingerprint, true, nil
	}
	return false, true, nil
}

func (s *Store) readReinstallLandingSource(ctx context.Context, tx *sql.Tx, agentID, preparationID string) (*AgentReinstallLandingSource, error) {
	var endpointID, phase string
	var identityJSON, targetsJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT endpoint_id,phase,identity_json,targets_json FROM agent_reinstall_landing_sources WHERE preparation_id=?`, preparationID).Scan(&endpointID, &phase, &identityJSON, &targetsJSON)
	saved := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !saved {
		err = tx.QueryRowContext(ctx, `SELECT e.id FROM meridian_endpoints e JOIN agent_reinstall_app_preparations p ON p.application_id=e.application_id WHERE p.deployment_id=? AND e.status<>'retired'`, preparationID).Scan(&endpointID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}

	result := &AgentReinstallLandingSource{State: "needs_review", Landings: []AgentReinstallLandingTarget{}}
	if saved {
		if json.Unmarshal(identityJSON, &result.Identity) != nil || json.Unmarshal(targetsJSON, &result.Landings) != nil {
			return nil, errExecutionAuthorization
		}
	}
	targets, err := reinstallLandingTargets(ctx, tx, endpointID)
	if err != nil {
		return result, nil
	}
	if !saved && len(targets) == 0 {
		return nil, nil
	}
	_, identity, _, validation := s.reinstallLandingIdentity(ctx, tx, agentID, preparationID)
	if !saved {
		result.Identity, result.Landings = identity, targets
		if validation == nil && identity.PreviousFingerprint != identity.CurrentFingerprint {
			result.State = "pending"
		}
		return result, nil
	}
	if validation != nil || len(targets) != len(result.Landings) || identity.CurrentFingerprint != result.Identity.CurrentFingerprint {
		return result, nil
	}
	expectedPin := result.Identity.PreviousFingerprint
	if phase == "authorize" {
		expectedPin = result.Identity.CurrentFingerprint
	}
	if identity.PreviousFingerprint != expectedPin {
		return result, nil
	}
	waiting := false
	for i, target := range targets {
		expected := result.Landings[i]
		if target.NodeID != expected.NodeID || target.PeerFingerprint != expected.PeerFingerprint || target.Revision < expected.Revision {
			return result, nil
		}
		var desired, applied []byte
		var revision int64
		var status, lease string
		var uncertain bool
		err = tx.QueryRowContext(ctx, `SELECT desired_json,applied_json,applied_revision,status,lease_expires_at,
 EXISTS(SELECT 1 FROM task_executions e WHERE e.agent_id=node_id AND e.task_id='landing-server-'||node_id||'-r'||desired_revision AND e.disposition='' AND (e.state IN ('failed','unknown') OR (e.phase='result_received' AND e.state<>'succeeded')))
 FROM landing_server_states WHERE node_id=?`, target.NodeID).Scan(&desired, &applied, &revision, &status, &lease, &uncertain)
		if err != nil {
			return nil, err
		}
		if uncertain || status == "failed" || status == "stopped" || status == "applying" && lease <= s.now().UTC().Format(time.RFC3339Nano) {
			return result, nil
		}
		var desiredState landing.ServerState
		if json.Unmarshal(desired, &desiredState) != nil || desiredState.Validate() != nil || desiredState.NodeID != target.NodeID || desiredState.Revision != uint64(target.Revision) || desiredState.Plan == nil {
			return result, nil
		}
		want := phase == "authorize"
		if meridianServerAuthorizesSource(desired, target.NodeID, result.Identity.CurrentAddress) != want || result.Identity.PreviousAddress != result.Identity.CurrentAddress && meridianServerAuthorizesSource(desired, target.NodeID, result.Identity.PreviousAddress) {
			return result, nil
		}
		if status != "ready" || revision != target.Revision {
			waiting = true
			continue
		}
		var state landing.ServerState
		if json.Unmarshal(applied, &state) != nil || state.Validate() != nil || state.NodeID != target.NodeID || state.Revision != uint64(revision) || state.Plan == nil {
			return result, nil
		}
		if meridianServerAuthorizesSource(applied, target.NodeID, result.Identity.CurrentAddress) != want || result.Identity.PreviousAddress != result.Identity.CurrentAddress && meridianServerAuthorizesSource(applied, target.NodeID, result.Identity.PreviousAddress) {
			return result, nil
		}
	}
	if waiting {
		result.State = "withdrawing"
		if phase == "authorize" {
			result.State = "authorizing"
		}
	} else {
		result.State = "withdrawn"
		if phase == "authorize" {
			result.State = "authorized"
		}
	}
	return result, nil
}

func (s *Store) UpdateAgentReinstallLandingSource(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput, authorize bool) (AgentReinstallLandingSource, error) {
	var result AgentReinstallLandingSource
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var preparationID string
	var runtimeID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT p.deployment_id,p.runtime_command_id FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id WHERE op.agent_id=? AND op.id=? AND op.authorized_by=? AND op.state='review_required' AND p.application_id=?`, agentID, input.OperationID, adminID, input.ApplicationID).Scan(&preparationID, &runtimeID)
	if err != nil {
		return result, errExecutionAuthorization
	}
	endpointID, identity, currentJSON, err := s.reinstallLandingIdentity(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	previous, err := s.readReinstallLandingSource(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	if previous == nil {
		return result, errors.New("center: no saved landing grants to restore")
	}
	if !authorize && previous.State != "pending" || authorize && (previous.State == "authorizing" || previous.State == "authorized") {
		return *previous, nil
	}
	if runtimeID.Valid {
		return result, errors.New("center: landing identity replacement must precede runtime restoration")
	}
	if authorize && previous.State != "withdrawn" {
		return result, errors.New("center: wait for verified landing withdrawal receipts")
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if len(input.PlanRevision) != 64 || plan.Revision != input.PlanRevision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return result, errors.New("center: settle previous work before restoring landing access")
	}
	if err = ensureMeridianManagementWritable(ctx, tx); err != nil {
		return result, err
	}
	for _, target := range previous.Landings {
		if target.NodeID == agentID {
			return result, errors.New("center: restore the landing server separately before replacing its source")
		}
		if blocked, e := agentReinstallBlocked(ctx, tx, target.NodeID); e != nil || blocked {
			return result, errExecutionBlocked
		}
		var settled bool
		err = tx.QueryRowContext(ctx, `SELECT status='ready' AND desired_revision=applied_revision AND NOT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') AND NOT EXISTS(SELECT 1 FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) AND NOT EXISTS(SELECT 1 FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) FROM landing_server_states WHERE node_id=?`, target.NodeID, target.NodeID, target.NodeID, target.NodeID).Scan(&settled)
		if err != nil || !settled {
			return result, errors.New("center: settle affected landing work before identity replacement")
		}
	}
	if authorize {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_landing_sources SET phase='authorize',authorized_revision=? WHERE preparation_id=? AND phase='withdraw'`, input.PlanRevision, preparationID); err != nil {
			return result, err
		}
		// The common helper requires applied removal on every landing and keeps all
		// existing credentials. It queues new authorization, never invents a receipt.
		if _, err = s.replaceMeridianSourceInTx(ctx, tx, endpointID, agentID, adminID, identity, currentJSON); err != nil {
			return result, err
		}
	} else {
		if identity.PreviousFingerprint == identity.CurrentFingerprint {
			return result, errors.New("center: replacement identity must differ from the previous source")
		}
		encoded, _ := json.Marshal(identity)
		targets, _ := json.Marshal(previous.Landings)
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_landing_sources(preparation_id,endpoint_id,phase,identity_json,targets_json,plan_revision) VALUES(?,?,'withdraw',?,?,?)`, preparationID, endpointID, encoded, targets, input.PlanRevision); err != nil {
			return result, err
		}
		for _, target := range previous.Landings {
			if err = s.refreshClientLandingSources(ctx, tx, target.NodeID); err != nil {
				return result, err
			}
		}
	}
	targets, err := reinstallLandingTargets(ctx, tx, endpointID)
	if err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(targets)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_landing_sources SET targets_json=? WHERE preparation_id=?`, encoded, preparationID); err != nil {
		return result, err
	}
	updated, err := s.readReinstallLandingSource(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	if updated == nil || updated.State == "needs_review" {
		return result, errors.New("center: shared landing authorization conflicts with the reviewed identity; no changes applied")
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, preparationID); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, agentID, "agent.reinstall.landing", 0, "queued", "Reviewed landing identity update queued; server and client verification remain pending"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	for _, target := range targets {
		s.taskChanges.notify("agent:" + target.NodeID)
	}
	return *updated, nil
}

func (s *Server) handleAgentReinstallLandingSource(w http.ResponseWriter, r *http.Request) {
	var input AgentReinstallApplicationInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.UpdateAgentReinstallLandingSource(r.Context(), r.PathValue("id"), admin, input, strings.HasSuffix(r.URL.Path, "/authorize-landing"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
