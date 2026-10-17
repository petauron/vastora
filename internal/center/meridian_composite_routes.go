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

// Creation validates both existing exits without granting authority. Runtime
// projection and publication additionally demand their applied source grants.
func (s *Store) readCompositeRoutePeers(ctx context.Context, tx *sql.Tx, endpointID, ipv6ID, ipv4ID string, authorized bool) ([]glivela.RoutePeer, []meridianruntime.Peer, error) {
	var entryID string
	var sourceJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id,endpoint.source_peer_json FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id WHERE endpoint.id=? AND endpoint.status<>'retired'`, endpointID).Scan(&entryID, &sourceJSON); err != nil {
		return nil, nil, errors.New("center: composite entry is unavailable")
	}
	if ipv6ID == entryID || ipv4ID == entryID || ipv6ID == ipv4ID {
		return nil, nil, errors.New("center: composite exits must be two distinct landing nodes, separate from the entry")
	}
	var source landing.PeerIdentity
	if json.Unmarshal(sourceJSON, &source) != nil || (meridianruntime.Peer{EgressID: entryID, Identity: source}).Validate() != nil {
		return nil, nil, errors.New("center: composite entry has no pinned private identity")
	}
	peers := make([]glivela.RoutePeer, 0, 2)
	runtimePeers := make([]meridianruntime.Peer, 0, 2)
	for _, id := range []string{ipv6ID, ipv4ID} {
		var appliedJSON, desiredJSON, peerJSON []byte
		var revision uint64
		if err := tx.QueryRowContext(ctx, `SELECT server.applied_json,server.desired_json,server.peer_json,server.applied_revision
			FROM landing_server_states server JOIN agents agent ON agent.id=server.node_id
			WHERE server.node_id=? AND server.applied_revision>0 AND server.status IN ('ready','pending','applying')
			AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?`,
			id, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&appliedJSON, &desiredJSON, &peerJSON, &revision); err != nil {
			return nil, nil, errors.New("center: both composite landing Agents must be online with an applied service")
		}
		var applied, desired landing.ServerState
		var identity landing.PeerIdentity
		if json.Unmarshal(appliedJSON, &applied) != nil || json.Unmarshal(desiredJSON, &desired) != nil || json.Unmarshal(peerJSON, &identity) != nil ||
			applied.Validate() != nil || desired.Validate() != nil || applied.NodeID != id || desired.NodeID != id || applied.Revision != revision || applied.Plan == nil || desired.Plan == nil ||
			applied.Plan.Address != identity.Address || desired.Plan.Address != identity.Address || applied.Plan.EgressIP != desired.Plan.EgressIP ||
			(meridianruntime.Peer{EgressID: id, Identity: identity}).Validate() != nil {
			return nil, nil, errors.New("center: composite landing identity or applied exit family changed")
		}
		if identity.ID == source.ID || identity.PublicKey == source.PublicKey || identity.Address == source.Address {
			return nil, nil, errors.New("center: composite landing is the entry machine")
		}
		for _, previous := range runtimePeers {
			if previous.Identity.ID == identity.ID || previous.Identity.PublicKey == identity.PublicKey {
				return nil, nil, errors.New("center: composite landing identities must be distinct")
			}
		}
		if authorized && !meridianLandingSourceAuthorized(appliedJSON, desiredJSON, id, revision, source, identity) {
			return nil, nil, errors.New("center: both composite exits must confirm the exact entry source authorization")
		}
		family := glivela.EgressIPv4Only // Default Dante plan explicitly uses IPv4.
		if applied.Plan.EgressIP != "" {
			address, err := netip.ParseAddr(applied.Plan.EgressIP)
			if err != nil || address.Is4In6() || address.Is6() && !glivela.CompositePublicIP(address) {
				return nil, nil, errors.New("center: invalid composite exit address")
			}
			if address.Is6() {
				family = glivela.EgressIPv6Only
			}
		}
		peers = append(peers, glivela.RoutePeer{EgressID: id, Address: identity.Address, Port: landing.SOCKSPort, ExitFamily: family, Ready: true})
		runtimePeers = append(runtimePeers, meridianruntime.Peer{EgressID: id, Identity: identity})
	}
	if err := glivela.ValidateCompositeExitPair(peers[0], peers[1]); err != nil {
		return nil, nil, err
	}
	return peers, runtimePeers, nil
}
