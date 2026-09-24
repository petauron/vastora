package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

const meridianRouteEvidenceUnavailable = "Waiting for fresh verified entry-to-egress transport; this fixed egress remains blocked."

// Pin the private entry identity only as part of explicit first-route creation.
// Later heartbeats and task rebuilds must not authorize a replacement machine
// merely because it acquired the same tailnet address.
func (s *Store) authorizeMeridianEntrySource(ctx context.Context, tx *sql.Tx, endpointID string) error {
	var pinnedJSON, observedJSON []byte
	var agentID string
	err := tx.QueryRowContext(ctx, `SELECT endpoint.source_peer_json,capability.peer_json,agent.id
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		JOIN agents agent ON agent.id=application.node_id
		JOIN agent_private_peer_capabilities capability ON capability.node_id=agent.id
		WHERE endpoint.id=? AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed'
		AND capability.generation=? AND capability.observed_at>?`, endpointID, landing.ClientRuntimeGeneration,
		s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano)).Scan(&pinnedJSON, &observedJSON, &agentID)
	var observed, pinned landing.PeerIdentity
	if err != nil || json.Unmarshal(observedJSON, &observed) != nil || (meridianruntime.Peer{EgressID: agentID, Identity: observed}).Validate() != nil || json.Unmarshal(pinnedJSON, &pinned) != nil {
		return errors.New("center: Meridian entry private identity is unavailable; wait for its authenticated Agent report")
	}
	if pinned != (landing.PeerIdentity{}) {
		if pinned != observed {
			return errors.New("center: Meridian entry private identity changed; explicitly reconcile the replaced machine before authorizing routes")
		}
		return nil
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants WHERE endpoint_id=?`, endpointID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("center: existing Meridian routes have no pinned entry identity; explicit reconciliation is required")
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET source_peer_json=? WHERE id=?`, observedJSON, endpointID)
	return err
}

// A container receipt establishes the endpoint, not its remote egresses. Each
// route follows only its own authenticated peer's short-lived kernel lease.
// Readers also check the persisted deadline, so a lost heartbeat cannot leave
// the last healthy observation valid forever.
func recordMeridianRouteHealth(ctx context.Context, tx *sql.Tx, endpointID string, projection meridianRuntimeProjection, result meridianruntime.Result, now time.Time) error {
	health, err := result.PeerHealth(projection.task, now)
	if err != nil {
		return err
	}
	expires := make(map[string]int64, len(result.Peers))
	for _, peer := range result.Peers {
		if health[peer.EgressID] {
			expires[peer.EgressID] = peer.Status.AllowedUntil.UnixMilli()
		}
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, grantID := range projection.readyRouteGrantIDs {
		egressID, exists := projection.routePeerIDs[grantID]
		if !exists {
			return errors.New("center: Meridian route has no bound egress observation")
		}
		status, healthy, message := "blocked", 0, meridianRouteEvidenceUnavailable
		if health[egressID] {
			status, healthy, message = "ready", 1, ""
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants
			SET applied_revision=desired_revision,runtime_healthy=?,health_expires_unix_ms=?,status=?,last_error=?,updated_at=?
			WHERE id=? AND endpoint_id=? AND egress_node_id=? AND enabled=1 AND status NOT IN ('revoked','revoking')`,
			healthy, expires[egressID], status, message, stamp, grantID, endpointID, egressID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian route changed before its transport observation was committed")
		}
	}
	for grantID, message := range projection.blockedRouteGrants {
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=0,health_expires_unix_ms=0,status='blocked',last_error=?,updated_at=?
			WHERE id=? AND endpoint_id=? AND enabled=1 AND status NOT IN ('revoked','revoking')`, message, stamp, grantID, endpointID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian blocked route changed before its observation was committed")
		}
	}
	return nil
}
