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

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

var errMeridianSubscriptionNotFound = errors.New("center: Meridian subscription is unavailable")

type meridianSubscription struct {
	Account meridian.Account
	Entries []meridian.NativeEntry
	Routes  []meridian.PublishedRoute
	Usage   meridian.QuotaProjection
}

type meridianCredentialRecord struct {
	Material         meridian.CredentialMaterial
	RealityEndpoint  meridian.RealityEndpoint
	HysteriaEndpoint *meridian.HysteriaEndpoint
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
	var result meridianSubscription
	var accountSecretID, storedTokenHash, status, cutoverState, importSHA string
	var enabled int
	err = tx.QueryRowContext(ctx, `SELECT account.id,account.display_name,account.total_bytes,account.expiry_time,account.reset_days,account.enabled,
		account.subscription_token_secret_id,account.subscription_token_sha256,account.desired_revision,account.applied_revision,account.status,
		cutover.state,cutover.import_sha256
		FROM meridian_accounts account JOIN meridian_cutover cutover ON cutover.id=1
		WHERE account.subscription_token_sha256=? AND cutover.subscription_authority='meridian'`, hex.EncodeToString(digest[:])).Scan(
		&result.Account.ID, &result.Account.DisplayName, &result.Account.Plan.TotalBytes, &result.Account.Plan.ExpiryTime,
		&result.Account.Plan.ResetDays, &enabled, &accountSecretID, &storedTokenHash,
		&result.Account.DesiredRevision, &result.Account.AppliedRevision, &status, &cutoverState, &importSHA,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	if err != nil {
		return meridianSubscription{}, fmt.Errorf("center: read Meridian subscription account: %w", err)
	}
	result.Account.Plan.Enabled = enabled == 1
	cutoverSnapshot := len(importSHA) == 64 && (cutoverState == "publish" || cutoverState == "project" || cutoverState == "verify" || cutoverState == "retire")
	accountApplied := result.Account.DesiredRevision == result.Account.AppliedRevision || cutoverSnapshot
	if status != "active" || !result.Account.Plan.Enabled || result.Account.Plan.ExpiryTime > 0 && result.Account.Plan.ExpiryTime <= s.now().UnixMilli() {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	storedToken, err := s.meridianSecretInTx(ctx, tx, accountSecretID, meridianAccountSecretContext(result.Account.ID))
	if err != nil || subtle.ConstantTimeCompare(storedToken, []byte(token)) != 1 || storedTokenHash != hex.EncodeToString(digest[:]) {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	usage, err := s.meridianAccountUsage(ctx, tx, result.Account.ID)
	if err != nil {
		return meridianSubscription{}, err
	}
	result.Usage, err = meridian.ProjectQuota(result.Account.Plan, usage)
	if err != nil || !result.Usage.Enabled {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	projection, projectionErr := s.meridianSubscriptionProjectionInTx(ctx, tx, result.Account.ID, cutoverSnapshot)
	if projectionErr == nil {
		result.Entries, result.Routes = projection.Entries, projection.Routes
		if !cutoverSnapshot && accountApplied {
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
					return meridianSubscription{}, errors.New("center: applied Meridian subscription snapshot is stale")
				}
				return meridianSubscription{}, projectionErr
			}
			return meridianSubscription{}, errMeridianSubscriptionNotFound
		}
		snapshot, snapshotErr = s.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
		if snapshotErr != nil {
			return meridianSubscription{}, snapshotErr
		}
		result.Entries, result.Routes = snapshot.Entries, snapshot.Routes
	} else {
		return meridianSubscription{}, projectionErr
	}
	if len(result.Entries) == 0 {
		return meridianSubscription{}, errMeridianSubscriptionNotFound
	}
	if err := tx.Commit(); err != nil {
		return meridianSubscription{}, err
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
		if credential.Kind != meridian.NativeCredential || !credential.Enabled {
			continue
		}
		if record.VLESSEnabled {
			link, linkErr := meridian.LinkForCredential(record.RealityEndpoint, record.Material, record.EntryName)
			if linkErr != nil {
				return meridianAppliedSubscriptionSnapshot{}, errors.New("center: Meridian native VLESS subscription projection is invalid")
			}
			result.Entries = append(result.Entries, meridian.NativeEntry{Material: record.Material, Protocol: meridian.VLESSReality, Link: link})
		}
		if record.HY2Enabled && record.HysteriaEndpoint != nil {
			link, linkErr := meridian.Hysteria2LinkForCredential(*record.HysteriaEndpoint, record.Material, record.EntryName+" · HY2")
			if linkErr != nil {
				return meridianAppliedSubscriptionSnapshot{}, errors.New("center: Meridian native Hysteria subscription projection is invalid")
			}
			result.Entries = append(result.Entries, meridian.NativeEntry{Material: record.Material, Protocol: meridian.Hysteria2, Link: link})
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
	rows, err := tx.QueryContext(ctx, `SELECT credential.id,credential.kind,credential.user_name,credential.identity_sha256,
		credential.protocol_secret_id,credential.hy2_auth_secret_id,credential.hy2_identity_sha256,credential.egress_node_id,credential.enabled,
		endpoint.id,endpoint.application_id,endpoint.inbound_tag,endpoint.listen_port,endpoint.advertise_host,endpoint.advertise_port,
		endpoint.target,endpoint.server_names_json,endpoint.public_key,endpoint.short_ids_json,endpoint.fingerprint,
		endpoint.vless_enabled,endpoint.hy2_enabled,endpoint.hy2_inbound_tag,endpoint.hy2_server_name,endpoint.hy2_certificate_not_after,
		service.display_name,service.name
		FROM meridian_credentials credential
		JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		JOIN publications publication ON publication.service_id=endpoint.service_id AND publication.kind='public_shared_443'
			AND ((?=1 AND publication.status<>'stopped') OR (publication.status='ready' AND publication.desired_revision=publication.applied_revision))
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		WHERE credential.account_id=? AND endpoint.quota_applied_enabled=1
		AND (endpoint.total_bytes=0 OR endpoint.used_bytes<endpoint.total_bytes) AND (
			(?=1 AND service.status<>'stopped' AND endpoint.status<>'retired')
			OR (service.status='ready' AND endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision)
		)
		ORDER BY endpoint.application_id,credential.kind,credential.id`, snapshot, accountID, snapshot)
	if err != nil {
		return nil, fmt.Errorf("center: read Meridian subscription credentials: %w", err)
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
		var serviceName string
		if err := rows.Scan(&value.record.Material.Credential.ID, &value.record.Material.Credential.Kind, &value.record.Material.Credential.User, &value.record.Material.Credential.Identity,
			&value.protocolSecretID, &value.hy2AuthSecretID, &value.record.Material.HysteriaIdentity, &egressNodeID, &enabled,
			&endpointID, &value.record.Material.Credential.EntryID, &value.record.RealityEndpoint.InboundTag, &value.record.RealityEndpoint.ListenPort,
			&value.record.RealityEndpoint.AdvertiseHost, &value.record.RealityEndpoint.AdvertisePort, &value.record.RealityEndpoint.Target, &value.serverNamesJSON,
			&value.record.RealityEndpoint.PublicKey, &value.shortIDsJSON, &value.record.RealityEndpoint.Fingerprint,
			&vlessEnabled, &hy2Enabled, &value.hy2InboundTag, &value.hy2ServerName, &value.hy2NotAfter,
			&value.record.EntryName, &serviceName); err != nil {
			rows.Close()
			return nil, err
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
			return nil, errors.New("center: stored Meridian REALITY material is invalid")
		}
		protocolID, err := s.meridianSecretInTx(ctx, tx, value.protocolSecretID, meridianCredentialSecretContext(record.Material.Credential.ID))
		if err != nil {
			return nil, errors.New("center: stored Meridian credential is unavailable")
		}
		record.Material.ProtocolID = string(protocolID)
		if value.hy2AuthSecretID.Valid {
			hy2Auth, secretErr := s.meridianSecretInTx(ctx, tx, value.hy2AuthSecretID.String, meridianCredentialHY2SecretContext(record.Material.Credential.ID))
			if secretErr != nil {
				return nil, errors.New("center: stored Meridian Hysteria credential is unavailable")
			}
			record.Material.HysteriaAuth = string(hy2Auth)
		}
		if value.hy2InboundTag != "" {
			expiresAt, expiryErr := time.Parse(time.RFC3339Nano, value.hy2NotAfter)
			if record.HY2Enabled && (expiryErr != nil || !expiresAt.After(s.now().Add(time.Hour))) {
				return nil, errors.New("center: stored Meridian Hysteria certificate is unavailable or expired")
			}
			record.HysteriaEndpoint = &meridian.HysteriaEndpoint{ID: record.RealityEndpoint.ID + "-hy2", EntryID: record.Material.Credential.EntryID, AdvertiseHost: record.RealityEndpoint.AdvertiseHost, AdvertisePort: 443, ServerName: value.hy2ServerName}
		}
		if record.Material.Validate() != nil || record.HY2Enabled && (record.HysteriaEndpoint == nil || record.Material.Credential.Kind == meridian.NativeCredential && record.Material.HysteriaAuth == "") {
			return nil, errors.New("center: stored Meridian credential is invalid")
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) meridianAccountUsage(ctx context.Context, queryer meridianSubscriptionQueryer, accountID string) ([]meridian.UsageMember, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT credential.id,watermark.baseline_bytes,watermark.observed_bytes,credential.enabled
		FROM meridian_credentials credential LEFT JOIN meridian_usage_watermarks watermark ON watermark.credential_id=credential.id
		WHERE credential.account_id=? ORDER BY credential.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("center: read Meridian usage watermarks: %w", err)
	}
	defer rows.Close()
	usage := []meridian.UsageMember{}
	for rows.Next() {
		var member meridian.UsageMember
		var baseline, observed sql.NullInt64
		var active int
		if err := rows.Scan(&member.CredentialID, &baseline, &observed, &active); err != nil {
			return nil, err
		}
		if !baseline.Valid || !observed.Valid {
			return nil, errors.New("center: Meridian usage watermark is missing")
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

func (s *Store) meridianPublishedRoutes(ctx context.Context, tx *sql.Tx, accountID string, credentials map[string]meridianCredentialRecord, cutoverSnapshot bool) ([]meridian.PublishedRoute, error) {
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		// Region metadata decorates routed subscription names; it is not part of
		// the route authority or transport identity. A damaged or temporarily
		// unavailable global landing preference must never take healthy native
		// entries (or already verified explicit route grants) offline.
		selection = LandingSelection{LandingRegionCodes: map[string]string{}}
	}
	snapshot := boolInt(cutoverSnapshot)
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.endpoint_id,grant_row.egress_node_id,grant_row.base_credential_id,grant_row.route_credential_id,
		grant_row.mode,grant_row.hide_native,grant_row.enabled,grant_row.desired_revision,grant_row.applied_revision,grant_row.runtime_healthy,grant_row.status,
		endpoint.applied_revision,endpoint.source_peer_json,landing.applied_json,landing.desired_json,landing.peer_json,
		landing.applied_revision,landing.desired_revision,landing.status
		FROM meridian_route_grants grant_row JOIN meridian_endpoints endpoint ON endpoint.id=grant_row.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		JOIN publications publication ON publication.service_id=endpoint.service_id AND publication.kind='public_shared_443'
			AND ((?=1 AND publication.status<>'stopped') OR (publication.status='ready' AND publication.desired_revision=publication.applied_revision))
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		JOIN landing_server_states landing ON landing.node_id=grant_row.egress_node_id
			AND landing.status IN ('ready','pending','applying')
		JOIN agents egress_agent ON egress_agent.id=grant_row.egress_node_id
			AND egress_agent.status='active' AND egress_agent.credential_revoked_at=''
			AND egress_agent.tailscale_ownership='managed' AND egress_agent.last_seen_at>?
		WHERE grant_row.account_id=? AND grant_row.enabled=1 AND endpoint.quota_applied_enabled=1
		AND (endpoint.total_bytes=0 OR endpoint.used_bytes<endpoint.total_bytes) AND (
			(?=1 AND endpoint.applied_revision=0 AND service.status<>'stopped'
				AND grant_row.status NOT IN ('blocked','revoking','revoked') AND endpoint.status<>'retired')
			OR (grant_row.status='ready' AND grant_row.runtime_healthy=1 AND grant_row.desired_revision=grant_row.applied_revision
				AND grant_row.health_expires_unix_ms>?
				AND service.status='ready' AND endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision)
		)
		ORDER BY grant_row.endpoint_id,grant_row.egress_node_id,grant_row.id`, snapshot, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), accountID, snapshot, s.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("center: read Meridian route grants: %w", err)
	}
	defer rows.Close()
	routes := []meridian.PublishedRoute{}
	for rows.Next() {
		var grant meridian.RouteGrant
		var endpointID, baseID, routeID, status, landingStatus string
		var hideNative, enabled, runtimeHealthy int
		var endpointAppliedRevision, landingAppliedRevision, landingDesiredRevision uint64
		var sourceJSON, landingAppliedJSON, landingDesiredJSON, landingPeerJSON []byte
		if err := rows.Scan(&grant.ID, &endpointID, &grant.EgressID, &baseID, &routeID, &grant.Mode, &hideNative, &enabled, &grant.DesiredRev, &grant.AppliedRev, &runtimeHealthy, &status,
			&endpointAppliedRevision, &sourceJSON, &landingAppliedJSON, &landingDesiredJSON, &landingPeerJSON, &landingAppliedRevision, &landingDesiredRevision, &landingStatus); err != nil {
			return nil, err
		}
		legacySnapshot := cutoverSnapshot && endpointAppliedRevision == 0
		if legacySnapshot {
			// This historical snapshot retains the pre-switch read boundary; it
			// cannot authorize a newly added source from an unapplied plan.
			if landingStatus != "ready" || landingDesiredRevision != landingAppliedRevision {
				continue
			}
		} else {
			var source, peer landing.PeerIdentity
			if json.Unmarshal(sourceJSON, &source) != nil || json.Unmarshal(landingPeerJSON, &peer) != nil ||
				!meridianLandingSourceAuthorized(landingAppliedJSON, landingDesiredJSON, grant.EgressID, landingAppliedRevision, source, peer) {
				continue
			}
		}
		base, baseOK := credentials[baseID]
		route, routeOK := credentials[routeID]
		if !baseOK || !routeOK || base.RealityEndpoint.ID != endpointID || route.RealityEndpoint.ID != endpointID {
			return nil, errors.New("center: stored Meridian route credentials are incomplete")
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
			baseLink, linkErr := meridian.LinkForCredential(base.RealityEndpoint, base.Material, base.EntryName)
			if linkErr != nil {
				return nil, errors.New("center: stored Meridian native VLESS route is invalid")
			}
			routeLink, linkErr := meridian.LinkForCredential(route.RealityEndpoint, route.Material, base.EntryName)
			if linkErr != nil {
				return nil, errors.New("center: stored Meridian routed VLESS credential is invalid")
			}
			routes = append(routes, meridian.PublishedRoute{Grant: grant, Protocol: meridian.VLESSReality, EntryName: base.EntryName, EgressRegionCode: selection.LandingRegionCodes[grant.EgressID], BaseLink: baseLink, RouteLink: routeLink, BaseProtocolIdentity: base.Material.Credential.Identity, RouteProtocolIdentity: route.Material.Credential.Identity})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}
