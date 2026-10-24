package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/glivela"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

const meridianRouteEvidenceUnavailable = "Waiting for fresh verified entry-to-egress transport; this route remains blocked."

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
		return errors.New("center: Glivela entry private identity is unavailable; wait for its authenticated Agent report")
	}
	if pinned != (landing.PeerIdentity{}) {
		if pinned != observed {
			return errors.New("center: Glivela entry private identity changed; explicitly reconcile the replaced machine before authorizing routes")
		}
		return nil
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants WHERE endpoint_id=?`, endpointID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("center: existing Glivela routes have no pinned entry identity; explicit reconciliation is required")
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET source_peer_json=? WHERE id=?`, observedJSON, endpointID)
	return err
}

// A container receipt establishes the endpoint, not its remote egresses. Each
// route requires its authenticated peer startup acceptance for this revision.
// Accepted evidence has no time-based traffic lease.
func recordMeridianRouteHealth(ctx context.Context, tx *sql.Tx, endpointID string, projection meridianRuntimeProjection, result meridianruntime.Result, now time.Time) error {
	health, err := result.PeerHealth(projection.task, now)
	if err != nil {
		return err
	}
	exits := make(map[string]netip.Addr, len(result.Peers))
	for _, peer := range result.Peers {
		exits[peer.EgressID], _ = netip.ParseAddr(peer.Status.ExitIP)
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, grantID := range projection.readyRouteGrantIDs {
		egressID, exists := projection.routePeerIDs[grantID]
		if !exists {
			return errors.New("center: Glivela route has no bound egress observation")
		}
		status, healthy, message := "blocked", 0, meridianRouteEvidenceUnavailable
		secondaryID := projection.routeSecondaryPeerIDs[grantID]
		familiesVerified := secondaryID == "" || exits[egressID].Is6() && glivela.CompositePublicIP(exits[egressID]) && exits[secondaryID].Is4() && glivela.CompositePublicIP(exits[secondaryID])
		if health[egressID] && (secondaryID == "" || health[secondaryID]) && familiesVerified {
			status, healthy, message = "ready", 1, ""
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants
			SET applied_revision=desired_revision,runtime_healthy=?,health_expires_unix_ms=?,status=?,last_error=?,updated_at=?
			WHERE id=? AND endpoint_id=? AND egress_node_id=? AND COALESCE(ipv4_egress_node_id,'')=? AND enabled=1 AND status NOT IN ('revoked','revoking')`,
			healthy, 0, status, message, stamp, grantID, endpointID, egressID, secondaryID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Glivela route changed before its transport observation was committed")
		}
		diagnostic := routeHealthDiagnostic(result, egressID, secondaryID, status, now)
		if !familiesVerified {
			diagnostic.Reason = "exit_family_mismatch"
		}
		if err := recordRouteHealthDiagnostics(ctx, tx, grantID, diagnostic); err != nil {
			return err
		}
	}
	for grantID, message := range projection.blockedRouteGrants {
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=0,health_expires_unix_ms=0,status='blocked',last_error=?,updated_at=?
			WHERE id=? AND endpoint_id=? AND enabled=1 AND status NOT IN ('revoked','revoking')`, message, stamp, grantID, endpointID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Glivela blocked route changed before its observation was committed")
		}
		if err := recordRouteHealthDiagnostics(ctx, tx, grantID, MeridianHealthDiagnostics{Status: "blocked", Reason: "authorization_unavailable", ReceivedAt: now.UTC(), ObservedAt: now.UTC(), Legs: []MeridianHealthLeg{}}); err != nil {
			return err
		}
	}
	return nil
}
