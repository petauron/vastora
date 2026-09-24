package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type meridianLegacyRouteCandidate struct {
	grant        meridian.RouteGrant
	protocolID   string
	endpointID   string
	credentialID string
}

// importReadyMeridianLegacyRoutesInTx moves only Center-authorized, applied
// fixed routes into the already imported Meridian account inventory. The
// Center grant holds the original child UUID and its activation receipt;
// creating a new UUID here would silently invalidate existing subscribers.
// Every route is checked before the first write so a bad grant cannot produce
// a partial migration. The caller's transaction also rolls back on failure.
func (s *Store) importReadyMeridianLegacyRoutesInTx(ctx context.Context, tx *sql.Tx, now time.Time) (int, error) {
	var state, authority string
	var expectedRoutes, existingRoutes int
	if err := tx.QueryRowContext(ctx, "SELECT state,subscription_authority,expected_routes FROM meridian_cutover WHERE id=1").
		Scan(&state, &authority, &expectedRoutes); err != nil {
		return 0, err
	}
	if state != "verify" || authority != "meridian" {
		return 0, errors.New("center: legacy routes can be imported only during Meridian verification")
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM meridian_route_grants").Scan(&existingRoutes); err != nil {
		return 0, err
	}
	if expectedRoutes != existingRoutes {
		return 0, errors.New("center: Meridian route inventory changed before legacy route import")
	}

	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT grant_row.id FROM landing_client_grants grant_row "+
		"JOIN meridian_endpoints endpoint ON endpoint.application_id=grant_row.application_id "+
		"WHERE grant_row.status='ready' AND NOT EXISTS(SELECT 1 FROM meridian_route_grants route WHERE route.id=grant_row.id) ORDER BY grant_row.id")
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	candidates := make([]meridianLegacyRouteCandidate, 0, len(ids))
	sources := map[string]landing.PeerIdentity{}
	for _, id := range ids {
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil || record.Status != "ready" || record.Revision != record.AppliedRevision ||
			!record.Grant.Enabled || record.Grant.Mode != landing.FixedMode || !record.MaterialSecretID.Valid {
			return 0, errors.New("center: ready legacy route has incomplete applied authority")
		}
		var endpointID, endpointTag, entryNodeID string
		var existingSourceJSON []byte
		var vlessEnabled int
		if err := tx.QueryRowContext(ctx, "SELECT endpoint.id,endpoint.inbound_tag,application.node_id,endpoint.source_peer_json,endpoint.vless_enabled "+
			"FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id "+
			"WHERE endpoint.application_id=? AND endpoint.service_id=? AND endpoint.status<>'retired'",
			record.ApplicationID, record.ServiceID).Scan(&endpointID, &endpointTag, &entryNodeID, &existingSourceJSON, &vlessEnabled); err != nil ||
			vlessEnabled != 1 || !sameLegacyInboundTag(endpointTag, record.Grant.InboundTag) {
			return 0, errors.New("center: ready legacy route no longer maps to a Meridian VLESS endpoint")
		}
		if (meridianruntime.Peer{EgressID: entryNodeID, Identity: record.Source}).Validate() != nil {
			return 0, errors.New("center: ready legacy route has no authorized entry source")
		}
		var existingSource landing.PeerIdentity
		if json.Unmarshal(existingSourceJSON, &existingSource) != nil ||
			existingSource != (landing.PeerIdentity{}) && existingSource != record.Source {
			return 0, errors.New("center: Meridian entry source differs from the authorized legacy source")
		}
		if previous, exists := sources[endpointID]; exists && previous != record.Source {
			return 0, errors.New("center: legacy routes disagree about their authorized entry source")
		}
		sources[endpointID] = record.Source

		var landingPeerJSON, appliedJSON, desiredJSON []byte
		var serverRevision uint64
		if err := tx.QueryRowContext(ctx, "SELECT server.peer_json,server.applied_json,server.desired_json,server.applied_revision FROM landing_server_states server JOIN agents agent ON agent.id=server.node_id "+
			"WHERE server.node_id=? AND server.status='ready' AND server.desired_revision=server.applied_revision "+
			"AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?",
			record.LandingNodeID, now.Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&landingPeerJSON, &appliedJSON, &desiredJSON, &serverRevision); err != nil {
			return 0, errors.New("center: ready legacy route landing is unavailable")
		}
		var landingPeer landing.PeerIdentity
		if json.Unmarshal(landingPeerJSON, &landingPeer) != nil || landingPeer != record.Grant.Peer {
			return 0, errors.New("center: ready legacy route landing identity changed")
		}
		if !meridianLandingSourceAuthorized(appliedJSON, desiredJSON, record.LandingNodeID, serverRevision, record.Source, landingPeer) {
			return 0, errors.New("center: ready legacy route landing no longer authorizes its entry")
		}

		var base meridian.Credential
		var accountName, accountStatus string
		var accountEnabled, baseEnabled int
		if err := tx.QueryRowContext(ctx, "SELECT credential.id,credential.account_id,credential.user_name,credential.identity_sha256,credential.enabled,"+
			"account.display_name,account.status,account.enabled FROM meridian_credentials credential "+
			"JOIN meridian_accounts account ON account.id=credential.account_id "+
			"WHERE credential.endpoint_id=? AND credential.kind='native' AND credential.identity_sha256=?",
			endpointID, record.ParentID).Scan(&base.ID, &base.AccountID, &base.User, &base.Identity, &baseEnabled,
			&accountName, &accountStatus, &accountEnabled); err != nil {
			return 0, errors.New("center: ready legacy route native account was not imported")
		}
		base.Kind, base.EntryID, base.Enabled = meridian.NativeCredential, record.ApplicationID, baseEnabled == 1
		if base.Validate() != nil || !base.Enabled || accountEnabled != 1 || accountStatus != "active" ||
			accountName != record.Grant.BaseUser || base.Identity != record.Grant.BaseIdentity {
			return 0, errors.New("center: ready legacy route parent authority changed")
		}

		protocolID, err := s.meridianSecretInTx(ctx, tx, record.CredentialSecretID, "landing-credential:"+record.ID)
		if err != nil || uuid.Validate(string(protocolID)) != nil || meridian.Identity(string(protocolID)) != record.Grant.FixedIdentity {
			return 0, errors.New("center: ready legacy route credential changed")
		}
		materialJSON, err := s.meridianSecretInTx(ctx, tx, record.MaterialSecretID.String, "landing-material:"+record.ID)
		if err != nil {
			return 0, errors.New("center: ready legacy route receipt is unavailable")
		}
		var material landing.ControllerResult
		if json.Unmarshal(materialJSON, &material) != nil || material.GrantID != record.ID ||
			material.Revision != record.Revision || material.Phase != "activate" ||
			material.BaseLink == "" || material.FixedLink == "" || material.SubscriptionToken == "" {
			return 0, errors.New("center: ready legacy route receipt changed")
		}

		credentialID, err := randomToken(18)
		if err != nil {
			return 0, err
		}
		route := meridian.Credential{
			ID: credentialID, AccountID: base.AccountID, Kind: meridian.RouteCredential,
			User: meridian.RouteUser(record.ID), Identity: meridian.Identity(string(protocolID)),
			EntryID: record.ApplicationID, EgressID: record.LandingNodeID, Enabled: true,
		}
		grant := meridian.RouteGrant{
			ID: record.ID, AccountID: base.AccountID, EntryID: record.ApplicationID,
			EgressID: record.LandingNodeID, InboundTag: endpointTag,
			Base: base, Route: route, Mode: meridian.FixedMode, Enabled: true,
			HideNative: record.Grant.HideBase, DesiredRev: 1,
		}
		if grant.Validate() != nil || (meridian.CredentialMaterial{Credential: route, ProtocolID: string(protocolID)}).Validate() != nil {
			return 0, errors.New("center: ready legacy route cannot be represented by Meridian")
		}
		candidates = append(candidates, meridianLegacyRouteCandidate{
			grant: grant, protocolID: string(protocolID),
			endpointID: endpointID, credentialID: credentialID,
		})
	}

	endpointIDs := make([]string, 0, len(sources))
	for endpointID := range sources {
		endpointIDs = append(endpointIDs, endpointID)
	}
	slices.Sort(endpointIDs)
	for _, endpointID := range endpointIDs {
		if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
			return 0, err
		}
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, candidate := range candidates {
		secretID, err := s.putSecret(ctx, tx, []byte(candidate.protocolID), meridianCredentialSecretContext(candidate.credentialID))
		if err != nil {
			return 0, err
		}
		route := candidate.grant.Route
		if _, err := tx.ExecContext(ctx, "INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at) "+
			"VALUES(?,?,?,?,?,?,?,?,1,?,?)", route.ID, route.AccountID, candidate.endpointID, route.Kind,
			route.User, route.Identity, secretID, route.EgressID, stamp, stamp); err != nil {
			return 0, fmt.Errorf("center: import legacy route credential: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) "+
			"VALUES(?,0,0,0,0,?)", route.ID, stamp); err != nil {
			return 0, err
		}
		grant := candidate.grant
		if _, err := tx.ExecContext(ctx, "INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,mode,hide_native,enabled,status,created_at,updated_at) "+
			"VALUES(?,?,?,?,?,?,'fixed',?,1,'pending',?,?)",
			grant.ID, grant.AccountID, candidate.endpointID, grant.EgressID, grant.Base.ID, grant.Route.ID,
			boolInt(grant.HideNative), stamp, stamp); err != nil {
			return 0, fmt.Errorf("center: import legacy route grant: %w", err)
		}
	}
	for _, endpointID := range endpointIDs {
		sourceJSON, err := json.Marshal(sources[endpointID])
		if err != nil {
			return 0, err
		}
		updated, err := tx.ExecContext(ctx, "UPDATE meridian_endpoints SET source_peer_json=?,desired_revision=desired_revision+1,"+
			"status='pending',runtime_healthy=0,last_error='',updated_at=? WHERE id=? AND status<>'retired'",
			sourceJSON, stamp, endpointID)
		if err != nil {
			return 0, err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return 0, errors.New("center: Meridian endpoint changed before legacy routes were imported")
		}
	}
	updated, err := tx.ExecContext(ctx, "UPDATE meridian_cutover SET expected_credentials=expected_credentials+?,expected_routes=expected_routes+?,"+
		"last_error='',updated_at=? WHERE id=1 AND state='verify' AND subscription_authority='meridian' AND expected_routes=?",
		len(candidates), len(candidates), stamp, expectedRoutes)
	if err != nil {
		return 0, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return 0, errors.New("center: Meridian cutover changed before legacy routes were imported")
	}
	return len(candidates), nil
}

func meridianUnimportedReadyLegacyRoutes(ctx context.Context, tx *sql.Tx) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM landing_client_grants grant_row "+
		"JOIN meridian_endpoints endpoint ON endpoint.application_id=grant_row.application_id "+
		"WHERE grant_row.status='ready' AND NOT EXISTS(SELECT 1 FROM meridian_route_grants route WHERE route.id=grant_row.id)").Scan(&count)
	return count, err
}
