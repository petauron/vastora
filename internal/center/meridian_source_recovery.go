package center

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// Fingerprints bind the operator's review to the complete peer identity without
// exposing private-network keys in the management UI or activity log.
type MeridianSourceRecoveryView struct {
	PreviousFingerprint string `json:"previousFingerprint"`
	CurrentFingerprint  string `json:"currentFingerprint"`
	PreviousAddress     string `json:"previousAddress"`
	CurrentAddress      string `json:"currentAddress"`
	EndpointRevision    int64  `json:"endpointRevision"`
	ObservedAt          string `json:"observedAt"`
}

type MeridianSourceRecoveryInput struct {
	PreviousFingerprint string `json:"previousFingerprint"`
	CurrentFingerprint  string `json:"currentFingerprint"`
	EndpointRevision    int64  `json:"endpointRevision"`
	ConfirmReplacement  bool   `json:"confirmReplacement"`
	ExecutionStopped    bool   `json:"executionStopped"`
}

func meridianPeerFingerprint(peer landing.PeerIdentity) string {
	encoded, _ := json.Marshal(peer)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (s *Store) meridianSourceRecoveryEvidence(ctx context.Context, queryer networkQueryer, endpointID string) (MeridianSourceRecoveryView, string, []byte, error) {
	var view MeridianSourceRecoveryView
	var previousJSON, currentJSON []byte
	var nodeID string
	err := queryer.QueryRowContext(ctx, `SELECT endpoint.source_peer_json,capability.peer_json,agent.id,endpoint.desired_revision,capability.observed_at
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		JOIN agents agent ON agent.id=application.node_id
		JOIN agent_private_peer_capabilities capability ON capability.node_id=agent.id
		WHERE endpoint.id=? AND application.app_key=? AND application.status='running'
		AND endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision
		AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed'
		AND capability.generation=? AND capability.observed_at>? AND capability.observed_at<=?`,
		endpointID, meridianAppKey, landing.ClientRuntimeGeneration, s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano),
		s.now().UTC().Format(time.RFC3339Nano)).Scan(&previousJSON, &currentJSON, &nodeID, &view.EndpointRevision, &view.ObservedAt)
	var previous, current landing.PeerIdentity
	if err != nil || json.Unmarshal(previousJSON, &previous) != nil || json.Unmarshal(currentJSON, &current) != nil ||
		(meridianruntime.Peer{EgressID: nodeID, Identity: previous}).Validate() != nil ||
		(meridianruntime.Peer{EgressID: nodeID, Identity: current}).Validate() != nil {
		return view, "", nil, errors.New("center: restore the native Meridian runtime and wait for a fresh authenticated private identity report before recovering landing access")
	}
	if previous == current {
		return view, "", nil, errors.New("center: this Meridian entry has no replacement private identity to recover")
	}
	view.PreviousFingerprint, view.CurrentFingerprint = meridianPeerFingerprint(previous), meridianPeerFingerprint(current)
	view.PreviousAddress, view.CurrentAddress = previous.Address, current.Address
	return view, nodeID, currentJSON, nil
}

func (s *Store) MeridianSourceRecovery(ctx context.Context, endpointID string) (MeridianSourceRecoveryView, error) {
	view, _, _, err := s.meridianSourceRecoveryEvidence(ctx, s.db, strings.TrimSpace(endpointID))
	return view, err
}

// RecoverMeridianSource explicitly replaces one durable entry identity. It is
// separate from runtime-journal recovery: all executions must already be settled
// and the old landing permissions must have been withdrawn by normal receipts.
func (s *Store) RecoverMeridianSource(ctx context.Context, endpointID, adminID string, input MeridianSourceRecoveryInput) error {
	endpointID, adminID = strings.TrimSpace(endpointID), strings.TrimSpace(adminID)
	if endpointID == "" || adminID == "" || !input.ConfirmReplacement || !input.ExecutionStopped {
		return errors.New("center: confirm the reinstalled node and that its previous execution has stopped")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return errors.New("center: administrator authorization required")
	}
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return err
	}
	view, nodeID, currentJSON, err := s.meridianSourceRecoveryEvidence(ctx, tx, endpointID)
	if err != nil {
		return err
	}
	if input.PreviousFingerprint != view.PreviousFingerprint || input.CurrentFingerprint != view.CurrentFingerprint || input.EndpointRevision != view.EndpointRevision {
		return errors.New("center: Meridian replacement identity or configuration changed; inspect it again before confirming")
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT egress_node_id FROM meridian_route_grants
		WHERE endpoint_id=? AND ((enabled=1 AND status<>'revoked') OR status='revoking') ORDER BY egress_node_id`, endpointID)
	if err != nil {
		return err
	}
	landingIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		landingIDs = append(landingIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range append([]string{nodeID}, landingIDs...) {
		var busy bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')
			OR EXISTS(SELECT 1 FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))
			OR EXISTS(SELECT 1 FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`, id, id, id).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return errors.New("center: settle active or uncertain executions on the entry and affected landings before identity recovery")
		}
	}
	for _, id := range landingIDs {
		var desiredJSON, appliedJSON []byte
		var desiredRevision, appliedRevision int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT desired_json,applied_json,desired_revision,applied_revision,status FROM landing_server_states WHERE node_id=?`, id).
			Scan(&desiredJSON, &appliedJSON, &desiredRevision, &appliedRevision, &status); err != nil {
			return err
		}
		var desired, applied landing.ServerState
		if json.Unmarshal(desiredJSON, &desired) != nil || json.Unmarshal(appliedJSON, &applied) != nil || desired.Validate() != nil || applied.Validate() != nil ||
			desired.NodeID != id || applied.NodeID != id || desired.Plan == nil || applied.Plan == nil ||
			status != "ready" || desiredRevision != appliedRevision || desired.Revision != uint64(desiredRevision) || applied.Revision != uint64(appliedRevision) {
			return errors.New("center: wait for the affected landing services to finish their current authorization update")
		}
		// Require the removal receipt even when the replacement reuses the old
		// address. Adding it back now necessarily needs a new server receipt.
		for _, raw := range [][]byte{desiredJSON, appliedJSON} {
			if meridianServerAuthorizesSource(raw, id, view.PreviousAddress) || meridianServerAuthorizesSource(raw, id, view.CurrentAddress) {
				return errors.New("center: wait for the landing services to confirm withdrawal of the previous entry identity")
			}
		}
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET source_peer_json=?,desired_revision=desired_revision+1,
		runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, currentJSON, now, endpointID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,health_expires_unix_ms=0,
		status=CASE WHEN status='revoking' THEN status ELSE 'blocked' END,last_error=?,updated_at=?
		WHERE endpoint_id=? AND ((enabled=1 AND status<>'revoked') OR status='revoking')`, meridianRouteSourceUnauthorized, now, endpointID); err != nil {
		return err
	}
	for _, id := range landingIDs {
		if err := s.refreshClientLandingSources(ctx, tx, id); err != nil {
			return err
		}
	}
	audit, _ := json.Marshal(map[string]any{"adminId": adminID, "previousFingerprint": view.PreviousFingerprint,
		"currentFingerprint": view.CurrentFingerprint, "observedAt": view.ObservedAt, "executionStopped": true})
	if err := s.recordTaskEvent(ctx, tx, endpointID, nodeID, "meridian.source.recover", view.EndpointRevision+1, "succeeded", string(audit)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, id := range append([]string{nodeID}, landingIDs...) {
		s.taskChanges.notify("agent:" + id)
	}
	return nil
}
