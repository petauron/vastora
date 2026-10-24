package center

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

var errMeridianSubscriptionNotFound = errors.New("center: Glivela subscription is unavailable")

type meridianSubscription struct {
	Account glivela.Account
	Entries []glivela.NativeEntry
	Routes  []glivela.PublishedRoute
	Usage   glivela.QuotaProjection
	Source  string
	Reason  string
}

type meridianCredentialRecord struct {
	Material         glivela.CredentialMaterial
	RealityEndpoint  glivela.RealityEndpoint
	HysteriaEndpoint *glivela.HysteriaEndpoint
	VLESSEnabled     bool
	HY2Enabled       bool
	EntryName        string
}

type meridianSubscriptionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func meridianAccountSecretContext(accountID string) string {
	return "meridian-account:" + accountID
}

func meridianCredentialSecretContext(credentialID string) string {
	return "meridian-credential:" + credentialID
}

func meridianCredentialHY2SecretContext(credentialID string) string {
	return "meridian-credential-hy2:" + credentialID
}

func meridianEndpointSecretContext(endpointID string) string {
	return "meridian-endpoint:" + endpointID
}

func meridianHY2CertificateSecretContext(endpointID string) string {
	return "meridian-hy2-certificate:" + endpointID
}

func meridianHY2PrivateKeySecretContext(endpointID string) string {
	return "meridian-hy2-private-key:" + endpointID
}

func (s *Store) MeridianSubscription(ctx context.Context, token string) (meridianSubscription, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 512 {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return meridianSubscription{}, err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(token))
	var accountID, secretID string
	err = tx.QueryRowContext(ctx, `SELECT id,subscription_token_secret_id FROM meridian_accounts WHERE subscription_token_sha256=?`, hex.EncodeToString(digest[:])).Scan(&accountID, &secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	if err != nil {
		return meridianSubscription{}, err
	}
	stored, err := s.meridianSecretInTx(ctx, tx, secretID, meridianAccountSecretContext(accountID))
	if err != nil || subtle.ConstantTimeCompare(stored, []byte(token)) != 1 {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	if err := s.refreshGlivelaPilotSubscriptionInTx(ctx, tx, accountID); err != nil {
		return meridianSubscription{}, err
	}
	result, err := s.resolveMeridianSubscriptionInTx(ctx, tx, accountID, true)
	if err != nil {
		return meridianSubscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return meridianSubscription{}, err
	}
	return result, nil
}

// Both public downloads and administrator previews resolve the same account,
// live projection and filtered applied snapshot. Previews never save snapshots.
func (s *Store) resolveMeridianSubscriptionInTx(ctx context.Context, tx *sql.Tx, accountID string, saveSnapshot bool) (meridianSubscription, error) {
	var result meridianSubscription
	var status, cutoverState, importSHA string
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT account.id,account.display_name,account.total_bytes,account.expiry_time,account.reset_days,account.enabled,
		account.desired_revision,account.applied_revision,account.status,
		cutover.state,cutover.import_sha256
		FROM meridian_accounts account JOIN meridian_cutover cutover ON cutover.id=1
		WHERE account.id=? AND cutover.subscription_authority='meridian'`, accountID).Scan(
		&result.Account.ID, &result.Account.DisplayName, &result.Account.Plan.TotalBytes, &result.Account.Plan.ExpiryTime,
		&result.Account.Plan.ResetDays, &enabled,
		&result.Account.DesiredRevision, &result.Account.AppliedRevision, &status, &cutoverState, &importSHA,
	)
	if errors.Is(err, sql.ErrNoRows) {
		result.Reason = "subscription_unavailable"
		return result, errMeridianSubscriptionNotFound
	}
	if err != nil {
		return meridianSubscription{}, fmt.Errorf("center: read Glivela subscription account: %w", err)
	}
	result.Account.Plan.Enabled = enabled == 1
	cutoverSnapshot := len(importSHA) == 64 && (cutoverState == "publish" || cutoverState == "project" || cutoverState == "verify" || cutoverState == "retire")
	accountApplied := result.Account.DesiredRevision == result.Account.AppliedRevision || cutoverSnapshot
	switch {
	case !result.Account.Plan.Enabled:
		result.Reason = "account_disabled"
	case result.Account.Plan.ExpiryTime > 0 && result.Account.Plan.ExpiryTime <= s.now().UnixMilli():
		result.Reason = "account_expired"
	case status != "active":
		result.Reason = "account_pending"
	}
	if result.Reason != "" {
		return result, errMeridianSubscriptionNotFound
	}
	usage, err := s.meridianAccountUsage(ctx, tx, result.Account.ID)
	if err != nil {
		if errors.Is(err, errMeridianSubscriptionNotFound) {
			result.Reason = "subscription_unavailable"
		}
		return result, err
	}
	result.Usage, err = glivela.ProjectQuota(result.Account.Plan, usage)
	if err != nil {
		result.Reason = "subscription_unavailable"
		return result, errMeridianSubscriptionNotFound
	}
	if !result.Usage.Enabled {
		result.Reason = "quota_exhausted"
		return result, errMeridianSubscriptionNotFound
	}
	projection, projectionErr := s.meridianSubscriptionProjectionInTx(ctx, tx, result.Account.ID, cutoverSnapshot)
	result.Source = "live"
	if cutoverSnapshot {
		result.Source = "imported_snapshot"
	}
	if projectionErr == nil {
		result.Entries, result.Routes = projection.Entries, projection.Routes
		if saveSnapshot && !cutoverSnapshot && accountApplied {
			fullyApplied, err := meridianSubscriptionEndpointsAppliedInTx(ctx, tx, result.Account.ID)
			if err != nil {
				return meridianSubscription{}, err
			}
			// A partial live response keeps already-ready entries available, but
			// must not erase the last complete applied subscription during a rebuild.
			if fullyApplied {
				if err := s.saveMeridianSubscriptionSnapshotInTx(ctx, tx, result.Account, projection); err != nil {
					return meridianSubscription{}, err
				}
			}
		}
	} else if !cutoverSnapshot {
		snapshot, snapshotErr := s.loadMeridianSubscriptionSnapshotInTx(ctx, tx, result.Account.ID)
		if snapshotErr != nil || snapshot.AccountRevision != result.Account.AppliedRevision {
			if accountApplied {
				if snapshotErr == nil {
					return meridianSubscription{}, errors.New("center: applied Glivela subscription snapshot is stale")
				}
				return result, projectionErr
			}
			result.Reason = "subscription_unavailable"
			return result, errMeridianSubscriptionNotFound
		}
		snapshot, snapshotErr = s.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
		if snapshotErr != nil {
			return meridianSubscription{}, snapshotErr
		}
		result.Entries, result.Routes = snapshot.Entries, snapshot.Routes
		result.Source = "applied_snapshot"
	} else {
		return result, projectionErr
	}
	if len(result.Entries) == 0 {
		result.Reason = "subscription_unavailable"
		return result, errMeridianSubscriptionNotFound
	}

	return result, nil
}

func (s *Store) meridianSubscriptionProjectionInTx(ctx context.Context, tx *sql.Tx, accountID string, cutoverSnapshot bool) (meridianAppliedSubscriptionSnapshot, error) {
	records, err := s.meridianSubscriptionCredentials(ctx, tx, accountID, cutoverSnapshot)
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	result := meridianAppliedSubscriptionSnapshot{AccountID: accountID}
	byID := make(map[string]meridianCredentialRecord, len(records))
	for _, record := range records {
		credential := record.Material.Credential
		byID[credential.ID] = record
		if credential.Kind != glivela.NativeCredential {
			continue
		}
		if !credential.Enabled {
			recordSubscriptionExclusion(ctx, MeridianSubscriptionExclusion{EndpointID: record.RealityEndpoint.ID, Name: record.EntryName, Reason: "credential_disabled"})
			continue
		}
		if !record.VLESSEnabled && !record.HY2Enabled {
			recordSubscriptionExclusion(ctx, MeridianSubscriptionExclusion{EndpointID: record.RealityEndpoint.ID, Name: record.EntryName, Reason: "protocol_disabled"})
		}
		if record.VLESSEnabled {
			link, linkErr := glivela.LinkForCredential(record.RealityEndpoint, record.Material, record.EntryName)
			if linkErr != nil {
				return meridianAppliedSubscriptionSnapshot{}, errors.New("center: Glivela native VLESS subscription projection is invalid")
			}
			result.Entries = append(result.Entries, glivela.NativeEntry{Material: record.Material, Protocol: glivela.VLESSReality, Link: link})
		}
		if record.HY2Enabled && record.HysteriaEndpoint != nil {
			link, linkErr := glivela.Hysteria2LinkForCredential(*record.HysteriaEndpoint, record.Material, record.EntryName+" · HY2")
			if linkErr != nil {
				return meridianAppliedSubscriptionSnapshot{}, errors.New("center: Glivela native Hysteria subscription projection is invalid")
			}
			result.Entries = append(result.Entries, glivela.NativeEntry{Material: record.Material, Protocol: glivela.Hysteria2, Link: link})
		}
	}
	if len(result.Entries) == 0 {
		return meridianAppliedSubscriptionSnapshot{}, errMeridianSubscriptionNotFound
	}
	result.Routes, err = s.meridianPublishedRoutes(ctx, tx, accountID, byID, cutoverSnapshot)
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	result, err = s.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, result)
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	if len(result.Entries) == 0 {
		return meridianAppliedSubscriptionSnapshot{}, errMeridianSubscriptionNotFound
	}
	return result, nil
}

func (s *Store) meridianSubscriptionCredentials(ctx context.Context, tx *sql.Tx, accountID string, cutoverSnapshot bool) ([]meridianCredentialRecord, error) {
	snapshot := boolInt(cutoverSnapshot)
	_, explain := ctx.Value(subscriptionDecisionsKey{}).(*[]MeridianSubscriptionExclusion)
	rows, err := tx.QueryContext(ctx, `SELECT credential.id,credential.kind,credential.user_name,credential.identity_sha256,
		credential.protocol_secret_id,credential.hy2_auth_secret_id,credential.hy2_identity_sha256,credential.egress_node_id,credential.enabled,
		endpoint.id,endpoint.application_id,endpoint.inbound_tag,endpoint.listen_port,endpoint.advertise_host,endpoint.advertise_port,
		endpoint.target,endpoint.server_names_json,endpoint.public_key,endpoint.short_ids_json,endpoint.fingerprint,
		endpoint.vless_enabled,endpoint.hy2_enabled,endpoint.hy2_inbound_tag,endpoint.hy2_server_name,endpoint.hy2_certificate_not_after,
		service.display_name,service.name,
 CASE
 WHEN publication.id IS NULL THEN 'publication_pending'
 WHEN endpoint.quota_applied_enabled<>1 OR (endpoint.total_bytes<>0 AND endpoint.used_bytes>=endpoint.total_bytes) THEN 'entry_quota_exhausted'
 WHEN NOT ((?=1 AND service.status<>'stopped' AND endpoint.status<>'retired') OR
 (service.status='ready' AND endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision)) THEN 'entry_pending'
 ELSE '' END AS subscription_exclusion_reason
		FROM meridian_credentials credential
		JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		LEFT JOIN publications publication ON publication.service_id=endpoint.service_id AND (publication.kind='public_shared_443' OR (publication.kind='public_direct' AND EXISTS(SELECT 1 FROM glivela_pilot_checkpoint native_owner JOIN applications native_app ON native_app.id=endpoint.application_id WHERE native_owner.endpoint_id=endpoint.id AND native_app.app_key='vastora-official/glivela-entry' AND native_app.runtime='systemd')))
			AND ((?=1 AND publication.status<>'stopped') OR (publication.status='ready' AND publication.desired_revision=publication.applied_revision))
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		WHERE credential.account_id=? AND (?=1 OR subscription_exclusion_reason='')
 ORDER BY endpoint.application_id,credential.kind,credential.id`, snapshot, snapshot, accountID, boolInt(explain))
	if err != nil {
		return nil, fmt.Errorf("center: read Glivela subscription credentials: %w", err)
	}
	type storedCredential struct {
		record           meridianCredentialRecord
		protocolSecretID string
		hy2AuthSecretID  sql.NullString
		hy2InboundTag    string
		hy2ServerName    string
		hy2NotAfter      string
		serverNamesJSON  []byte
		shortIDsJSON     []byte
	}
	stored := []storedCredential{}
	for rows.Next() {
		var value storedCredential
		var endpointID string
		var egressNodeID sql.NullString
		var enabled, vlessEnabled, hy2Enabled int
		var serviceName, exclusionReason string
		if err := rows.Scan(&value.record.Material.Credential.ID, &value.record.Material.Credential.Kind, &value.record.Material.Credential.User, &value.record.Material.Credential.Identity,
			&value.protocolSecretID, &value.hy2AuthSecretID, &value.record.Material.HysteriaIdentity, &egressNodeID, &enabled,
			&endpointID, &value.record.Material.Credential.EntryID, &value.record.RealityEndpoint.InboundTag, &value.record.RealityEndpoint.ListenPort,
			&value.record.RealityEndpoint.AdvertiseHost, &value.record.RealityEndpoint.AdvertisePort, &value.record.RealityEndpoint.Target, &value.serverNamesJSON,
			&value.record.RealityEndpoint.PublicKey, &value.shortIDsJSON, &value.record.RealityEndpoint.Fingerprint,
			&vlessEnabled, &hy2Enabled, &value.hy2InboundTag, &value.hy2ServerName, &value.hy2NotAfter,
			&value.record.EntryName, &serviceName, &exclusionReason); err != nil {
			rows.Close()
			return nil, err
		}
		if exclusionReason != "" {
			if value.record.Material.Credential.Kind == glivela.NativeCredential {
				name := value.record.EntryName
				if name == "" {
					name = serviceName
				}
				recordSubscriptionExclusion(ctx, MeridianSubscriptionExclusion{EndpointID: endpointID, Name: name, Reason: exclusionReason})
			}
			continue
		}
		value.record.Material.Credential.AccountID = accountID
		value.record.Material.Credential.Enabled = enabled == 1
		if egressNodeID.Valid {
			value.record.Material.Credential.EgressID = egressNodeID.String
		}
		value.record.RealityEndpoint.ID, value.record.RealityEndpoint.EntryID = endpointID, value.record.Material.Credential.EntryID
		value.record.VLESSEnabled, value.record.HY2Enabled = vlessEnabled == 1, hy2Enabled == 1
		if value.record.EntryName == "" {
			value.record.EntryName = serviceName
		}
		stored = append(stored, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records := []meridianCredentialRecord{}
	for _, value := range stored {
		record := value.record
		if json.Unmarshal(value.serverNamesJSON, &record.RealityEndpoint.ServerNames) != nil || json.Unmarshal(value.shortIDsJSON, &record.RealityEndpoint.ShortIDs) != nil {
			return nil, errors.New("center: stored Glivela REALITY material is invalid")
		}
		protocolID, err := s.meridianSecretInTx(ctx, tx, value.protocolSecretID, meridianCredentialSecretContext(record.Material.Credential.ID))
		if err != nil {
			return nil, errors.New("center: stored Glivela credential is unavailable")
		}
		record.Material.ProtocolID = string(protocolID)
		if value.hy2AuthSecretID.Valid {
			hy2Auth, secretErr := s.meridianSecretInTx(ctx, tx, value.hy2AuthSecretID.String, meridianCredentialHY2SecretContext(record.Material.Credential.ID))
			if secretErr != nil {
				return nil, errors.New("center: stored Glivela Hysteria credential is unavailable")
			}
			record.Material.HysteriaAuth = string(hy2Auth)
		}
		if value.hy2InboundTag != "" {
			expiresAt, expiryErr := time.Parse(time.RFC3339Nano, value.hy2NotAfter)
			if record.HY2Enabled && (expiryErr != nil || !expiresAt.After(s.now().Add(time.Hour))) {
				return nil, errors.New("center: stored Glivela Hysteria certificate is unavailable or expired")
			}
			record.HysteriaEndpoint = &glivela.HysteriaEndpoint{ID: record.RealityEndpoint.ID + "-hy2", EntryID: record.Material.Credential.EntryID, AdvertiseHost: record.RealityEndpoint.AdvertiseHost, AdvertisePort: 443, ServerName: value.hy2ServerName}
		}
		if record.Material.Validate() != nil || record.HY2Enabled && (record.HysteriaEndpoint == nil || record.Material.Credential.Kind == glivela.NativeCredential && record.Material.HysteriaAuth == "") {
			return nil, errors.New("center: stored Glivela credential is invalid")
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) meridianAccountUsage(ctx context.Context, queryer meridianSubscriptionQueryer, accountID string) ([]glivela.UsageMember, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT credential.id,watermark.baseline_bytes,watermark.observed_bytes,credential.enabled
		FROM meridian_credentials credential LEFT JOIN meridian_usage_watermarks watermark ON watermark.credential_id=credential.id
		WHERE credential.account_id=? ORDER BY credential.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("center: read Glivela usage watermarks: %w", err)
	}
	defer rows.Close()
	usage := []glivela.UsageMember{}
	for rows.Next() {
		var member glivela.UsageMember
		var baseline, observed sql.NullInt64
		var active int
		if err := rows.Scan(&member.CredentialID, &baseline, &observed, &active); err != nil {
			return nil, err
		}
		if !baseline.Valid || !observed.Valid {
			return nil, errors.New("center: Glivela usage watermark is missing")
		}
		member.Baseline, member.Observed, member.Active = baseline.Int64, observed.Int64, active == 1
		usage = append(usage, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(usage) == 0 {
		return nil, errMeridianSubscriptionNotFound
	}
	return usage, nil
}

func (s *Store) meridianPublishedRoutes(ctx context.Context, tx *sql.Tx, accountID string, credentials map[string]meridianCredentialRecord, cutoverSnapshot bool) ([]glivela.PublishedRoute, error) {
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		// Region metadata decorates routed subscription names; it is not part of
		// the route authority or transport identity. A damaged or temporarily
		// unavailable global landing preference must never take healthy native
		// entries (or already verified explicit route grants) offline.
		selection = LandingSelection{LandingRegionCodes: map[string]string{}}
	}
	snapshot := boolInt(cutoverSnapshot)
	_, explain := ctx.Value(subscriptionDecisionsKey{}).(*[]MeridianSubscriptionExclusion)
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.endpoint_id,grant_row.egress_node_id,COALESCE(grant_row.ipv4_egress_node_id,''),grant_row.base_credential_id,grant_row.route_credential_id,
		grant_row.mode,grant_row.hide_native,grant_row.enabled,grant_row.desired_revision,grant_row.applied_revision,grant_row.runtime_healthy,grant_row.status,
		endpoint.applied_revision,endpoint.source_peer_json,landing.applied_json,landing.desired_json,landing.peer_json,
		COALESCE(landing.applied_revision,0),COALESCE(landing.desired_revision,0),COALESCE(landing.status,''),
 service.display_name,COALESCE(egress_agent.name,''),
 CASE
 WHEN publication.id IS NULL THEN 'publication_pending'
 WHEN grant_row.enabled<>1 OR grant_row.status IN ('revoking','revoked') THEN 'route_revoked'
 WHEN endpoint.quota_applied_enabled<>1 OR (endpoint.total_bytes<>0 AND endpoint.used_bytes>=endpoint.total_bytes) THEN 'entry_quota_exhausted'
 WHEN landing.node_id IS NULL OR landing.status NOT IN ('ready','pending','applying') THEN 'landing_unavailable'
 WHEN egress_agent.id IS NULL OR egress_agent.status<>'active' OR egress_agent.credential_revoked_at<>'' OR egress_agent.tailscale_ownership<>'managed' OR egress_agent.last_seen_at<=? THEN 'landing_offline'
 WHEN ((?=1 AND endpoint.applied_revision=0 AND service.status<>'stopped' AND grant_row.status NOT IN ('blocked','revoking','revoked') AND endpoint.status<>'retired')
 OR (grant_row.status='ready' AND grant_row.runtime_healthy=1 AND grant_row.desired_revision=grant_row.applied_revision
 AND grant_row.health_expires_unix_ms>? AND service.status='ready' AND endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision)) THEN ''
 WHEN grant_row.health_expires_unix_ms<=? AND grant_row.status='ready' AND grant_row.runtime_healthy=1 AND grant_row.desired_revision=grant_row.applied_revision THEN 'health_expired'
 ELSE 'configuration_pending' END AS subscription_exclusion_reason
		FROM meridian_route_grants grant_row JOIN meridian_endpoints endpoint ON endpoint.id=grant_row.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		LEFT JOIN publications publication ON publication.service_id=endpoint.service_id AND (publication.kind='public_shared_443' OR (publication.kind='public_direct' AND EXISTS(SELECT 1 FROM glivela_pilot_checkpoint native_owner JOIN applications native_app ON native_app.id=endpoint.application_id WHERE native_owner.endpoint_id=endpoint.id AND native_app.app_key='vastora-official/glivela-entry' AND native_app.runtime='systemd')))
			AND ((?=1 AND publication.status<>'stopped') OR (publication.status='ready' AND publication.desired_revision=publication.applied_revision))
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
  LEFT JOIN landing_server_states landing ON landing.node_id=grant_row.egress_node_id
 LEFT JOIN agents egress_agent ON egress_agent.id=grant_row.egress_node_id
 WHERE grant_row.account_id=? AND (?=1 OR subscription_exclusion_reason='')
 ORDER BY grant_row.endpoint_id,grant_row.egress_node_id,grant_row.id`, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), snapshot, s.now().UnixMilli(), s.now().UnixMilli(), snapshot, accountID, boolInt(explain))
	if err != nil {
		return nil, fmt.Errorf("center: read Glivela route grants: %w", err)
	}
	defer rows.Close()
	routes := []glivela.PublishedRoute{}
	for rows.Next() {
		var grant glivela.RouteGrant
		var endpointID, baseID, routeID, status, landingStatus, entryName, egressName, exclusionReason string
		var hideNative, enabled, runtimeHealthy int
		var endpointAppliedRevision, landingAppliedRevision, landingDesiredRevision uint64
		var sourceJSON, landingAppliedJSON, landingDesiredJSON, landingPeerJSON []byte
		if err := rows.Scan(&grant.ID, &endpointID, &grant.EgressID, &grant.IPv4EgressID, &baseID, &routeID, &grant.Mode, &hideNative, &enabled, &grant.DesiredRev, &grant.AppliedRev, &runtimeHealthy, &status,
			&endpointAppliedRevision, &sourceJSON, &landingAppliedJSON, &landingDesiredJSON, &landingPeerJSON, &landingAppliedRevision, &landingDesiredRevision, &landingStatus, &entryName, &egressName, &exclusionReason); err != nil {
			return nil, err
		}
		if egressName != "" {
			entryName += " → " + egressName
		}
		exclude := func(reason string) {
			recordSubscriptionExclusion(ctx, MeridianSubscriptionExclusion{EndpointID: endpointID, RouteID: grant.ID, Name: entryName, Reason: reason})
		}
		if exclusionReason != "" {
			exclude(exclusionReason)
			continue
		}
		legacySnapshot := cutoverSnapshot && endpointAppliedRevision == 0
		if grant.IPv4EgressID != "" {
			if legacySnapshot {
				exclude("authorization_unavailable")
				continue
			}
			if _, _, err := s.readCompositeRoutePeers(ctx, tx, endpointID, grant.EgressID, grant.IPv4EgressID, true, true); err != nil {
				exclude("authorization_unavailable")
				continue
			}
		}
		if legacySnapshot {
			// This historical snapshot retains the pre-switch read boundary; it
			// cannot authorize a newly added source from an unapplied plan.
			if landingStatus != "ready" || landingDesiredRevision != landingAppliedRevision {
				exclude("configuration_pending")
				continue
			}
		} else {
			var source, peer landing.PeerIdentity
			if json.Unmarshal(sourceJSON, &source) != nil || json.Unmarshal(landingPeerJSON, &peer) != nil ||
				!meridianLandingSourceAuthorized(landingAppliedJSON, landingDesiredJSON, grant.EgressID, landingAppliedRevision, source, peer) {
				exclude("authorization_unavailable")
				continue
			}
		}
		base, baseOK := credentials[baseID]
		route, routeOK := credentials[routeID]
		if !baseOK || !routeOK || base.RealityEndpoint.ID != endpointID || route.RealityEndpoint.ID != endpointID {
			return nil, errors.New("center: stored Glivela route credentials are incomplete")
		}
		grant.AccountID, grant.EntryID, grant.InboundTag = accountID, base.Material.Credential.EntryID, base.RealityEndpoint.InboundTag
		grant.Base, grant.Route = base.Material.Credential, route.Material.Credential
		grant.HideNative, grant.Enabled, grant.RuntimeGood = hideNative == 1, enabled == 1, legacySnapshot || runtimeHealthy == 1 && status == "ready"
		if legacySnapshot {
			// This is the imported, still-running legacy publication, not a
			// Meridian receipt. Keep it renderable until this endpoint's first
			// applied runtime switches the route to fresh peer evidence.
			grant.AppliedRev = grant.DesiredRev
		}
		if base.VLESSEnabled {
			baseLink, linkErr := glivela.LinkForCredential(base.RealityEndpoint, base.Material, base.EntryName)
			if linkErr != nil {
				return nil, errors.New("center: stored Glivela native VLESS route is invalid")
			}
			routeLink, linkErr := glivela.LinkForCredential(route.RealityEndpoint, route.Material, base.EntryName)
			if linkErr != nil {
				return nil, errors.New("center: stored Glivela routed VLESS credential is invalid")
			}
			egressPrefix := ""
			if code := selection.LandingRegionCodes[grant.EgressID]; code != "" {
				if code == "TW" {
					egressPrefix = "🇨🇳 台湾"
				} else {
					egressPrefix = regionPrefix(code)
				}
			}
			ipv4Prefix := ""
			if code := selection.LandingRegionCodes[grant.IPv4EgressID]; code != "" {
				ipv4Prefix = regionPrefix(code)
			}
			routes = append(routes, glivela.PublishedRoute{Grant: grant, Protocol: glivela.VLESSReality, EntryName: base.EntryName, EgressRegionPrefix: egressPrefix, IPv4EgressRegionPrefix: ipv4Prefix, EgressNameSuffix: selection.LandingNameSuffixes[grant.EgressID], BaseLink: baseLink, RouteLink: routeLink, BaseProtocolIdentity: base.Material.Credential.Identity, RouteProtocolIdentity: route.Material.Credential.Identity})
		} else {
			exclude("route_protocol_disabled")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}
