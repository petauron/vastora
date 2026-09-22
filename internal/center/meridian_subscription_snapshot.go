package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/meridian"
)

type meridianAppliedSubscriptionSnapshot struct {
	AccountID       string                    `json:"accountId"`
	AccountRevision uint64                    `json:"accountRevision"`
	Entries         []meridian.NativeEntry    `json:"entries"`
	Routes          []meridian.PublishedRoute `json:"routes"`
}

func meridianSubscriptionSnapshotSecretContext(accountID string) string {
	return "meridian-subscription-snapshot:" + accountID
}

func validateMeridianSubscriptionSnapshot(value meridianAppliedSubscriptionSnapshot) error {
	if !meridian.ValidIdentifier(value.AccountID) || value.AccountRevision == 0 || len(value.Entries) == 0 {
		return errors.New("center: invalid Meridian subscription snapshot")
	}
	if _, err := meridian.RenderLinks(value.Entries, value.AccountID, meridian.FixedMode, value.Routes, false); err != nil {
		return errors.New("center: invalid Meridian subscription snapshot")
	}
	if _, err := meridian.RenderMihomo(value.Entries, value.AccountID, meridian.FixedMode, value.Routes); err != nil {
		return errors.New("center: invalid Meridian subscription snapshot")
	}
	return nil
}

func (s *Store) saveMeridianSubscriptionSnapshotInTx(ctx context.Context, tx *sql.Tx, account meridian.Account, value meridianAppliedSubscriptionSnapshot) error {
	value.AccountID = account.ID
	value.AccountRevision = account.AppliedRevision
	if account.DesiredRevision != account.AppliedRevision || validateMeridianSubscriptionSnapshot(value) != nil {
		return errors.New("center: cannot snapshot an unapplied Meridian subscription")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("center: cannot encode Meridian subscription snapshot")
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	var previousSecretID, previousFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT secret_id,content_sha256 FROM meridian_subscription_snapshots WHERE account_id=?`, account.ID).Scan(&previousSecretID, &previousFingerprint)
	if err == nil && previousFingerprint == fingerprint {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	secretID, err := s.putSecret(ctx, tx, encoded, meridianSubscriptionSnapshotSecretContext(account.ID))
	if err != nil {
		return err
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_subscription_snapshots(account_id,secret_id,content_sha256,account_revision,created_at,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET secret_id=excluded.secret_id,content_sha256=excluded.content_sha256,
		account_revision=excluded.account_revision,updated_at=excluded.updated_at`, account.ID, secretID, fingerprint, account.AppliedRevision, now, now); err != nil {
		return err
	}
	if previousSecretID != "" && previousSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, previousSecretID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadMeridianSubscriptionSnapshotInTx(ctx context.Context, tx *sql.Tx, accountID string) (meridianAppliedSubscriptionSnapshot, error) {
	var secretID, fingerprint string
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT secret_id,content_sha256,account_revision FROM meridian_subscription_snapshots WHERE account_id=?`, accountID).Scan(&secretID, &fingerprint, &revision); errors.Is(err, sql.ErrNoRows) {
		return meridianAppliedSubscriptionSnapshot{}, errMeridianSubscriptionNotFound
	} else if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	encoded, err := s.meridianSecretInTx(ctx, tx, secretID, meridianSubscriptionSnapshotSecretContext(accountID))
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, errors.New("center: stored Meridian subscription snapshot is unavailable")
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != fingerprint {
		return meridianAppliedSubscriptionSnapshot{}, errors.New("center: stored Meridian subscription snapshot changed")
	}
	var value meridianAppliedSubscriptionSnapshot
	if json.Unmarshal(encoded, &value) != nil || value.AccountID != accountID || value.AccountRevision != revision || validateMeridianSubscriptionSnapshot(value) != nil {
		return meridianAppliedSubscriptionSnapshot{}, errors.New("center: stored Meridian subscription snapshot is invalid")
	}
	return value, nil
}

// meridianSubscriptionEndpointsAppliedInTx distinguishes a complete applied
// inventory from the deliberately partial live output while an entry rebuilds.
func meridianSubscriptionEndpointsAppliedInTx(ctx context.Context, tx *sql.Tx, accountID string) (bool, error) {
	var applied bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM meridian_credentials credential
		JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		WHERE credential.account_id=? AND credential.kind='native' AND credential.enabled=1 AND endpoint.status<>'retired'
		AND (endpoint.status<>'ready' OR endpoint.runtime_healthy<>1 OR endpoint.desired_revision<>endpoint.applied_revision
		 OR service.status<>'ready' OR NOT EXISTS (
			SELECT 1 FROM publications publication WHERE publication.service_id=endpoint.service_id
			AND publication.kind='public_shared_443' AND publication.status='ready'
			AND publication.desired_revision=publication.applied_revision AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		 ))
	)`, accountID).Scan(&applied)
	return applied, err
}

// filterMeridianSubscriptionSnapshotRoutesInTx keeps the last applied native
// inventory available during rebuilds, while honoring explicit removal of a
// credential, endpoint, publication, or protocol. Fixed routes also require a
// live landing and unexpired entry-to-egress evidence; a hidden native stays
// hidden when its route is unavailable. Only an imported endpoint that has
// never applied Meridian may retain its existing legacy route during cutover.
func (s *Store) filterMeridianSubscriptionSnapshotRoutesInTx(ctx context.Context, tx *sql.Tx, value meridianAppliedSubscriptionSnapshot) (meridianAppliedSubscriptionSnapshot, error) {
	nativeRows, err := tx.QueryContext(ctx, `SELECT credential.id,endpoint.vless_enabled,endpoint.hy2_enabled
		FROM meridian_credentials credential
		JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
		JOIN services service ON service.id=endpoint.service_id
		WHERE credential.account_id=? AND credential.kind='native' AND credential.enabled=1
		AND endpoint.status<>'retired' AND service.status<>'stopped'
		AND EXISTS (
			SELECT 1 FROM publications publication WHERE publication.service_id=endpoint.service_id
			AND publication.kind='public_shared_443' AND publication.status<>'stopped'
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		)`, value.AccountID)
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	type nativeState struct{ vless, hy2 bool }
	nativeStates := map[string]nativeState{}
	for nativeRows.Next() {
		var credentialID string
		var state nativeState
		if err := nativeRows.Scan(&credentialID, &state.vless, &state.hy2); err != nil {
			nativeRows.Close()
			return meridianAppliedSubscriptionSnapshot{}, err
		}
		nativeStates[credentialID] = state
	}
	if err := nativeRows.Err(); err != nil {
		nativeRows.Close()
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	if err := nativeRows.Close(); err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.egress_node_id,grant_row.base_credential_id,grant_row.hide_native,grant_row.enabled,grant_row.status,
		CASE WHEN grant_row.enabled=1 AND grant_row.status NOT IN ('blocked','revoking','revoked')
		 AND ((endpoint.applied_revision=0 AND length(cutover.import_sha256)=64
			AND cutover.state IN ('publish','project','verify','retire'))
		  OR (grant_row.status='ready' AND grant_row.runtime_healthy=1
			AND grant_row.desired_revision=grant_row.applied_revision AND grant_row.health_expires_unix_ms>?))
		 AND landing.status='ready' AND landing.desired_revision=landing.applied_revision
		 AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?
		 THEN 1 ELSE 0 END
		FROM meridian_route_grants grant_row
		JOIN meridian_endpoints endpoint ON endpoint.id=grant_row.endpoint_id
		JOIN meridian_cutover cutover ON cutover.id=1
		LEFT JOIN landing_server_states landing ON landing.node_id=grant_row.egress_node_id
		LEFT JOIN agents agent ON agent.id=grant_row.egress_node_id
		WHERE grant_row.account_id=? ORDER BY grant_row.id`, s.now().UnixMilli(), s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), value.AccountID)
	if err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	type routeState struct {
		egressID string
		live     bool
	}
	states := map[string]routeState{}
	hiddenVLESSCredentials := map[string]bool{}
	for rows.Next() {
		var grantID, egressID, baseID, status string
		var hideNative, enabled, live int
		if err := rows.Scan(&grantID, &egressID, &baseID, &hideNative, &enabled, &status, &live); err != nil {
			rows.Close()
			return meridianAppliedSubscriptionSnapshot{}, err
		}
		mustHide := enabled == 1 && hideNative == 1 && status != "revoking" && status != "revoked" && live != 1
		states[grantID] = routeState{egressID: egressID, live: live == 1}
		if mustHide {
			hiddenVLESSCredentials[baseID] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return meridianAppliedSubscriptionSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return meridianAppliedSubscriptionSnapshot{}, err
	}

	routes := make([]meridian.PublishedRoute, 0, len(value.Routes))
	for _, route := range value.Routes {
		state, exists := states[route.Grant.ID]
		if exists && state.live && state.egressID == route.Grant.EgressID {
			routes = append(routes, route)
			continue
		}
		if !exists && route.Grant.HideNative {
			hiddenVLESSCredentials[route.Grant.Base.ID] = true
		}
	}
	entries := make([]meridian.NativeEntry, 0, len(value.Entries))
	vlessBases := map[string]bool{}
	for _, entry := range value.Entries {
		native, exists := nativeStates[entry.Material.Credential.ID]
		if !exists || entry.Protocol == meridian.VLESSReality && !native.vless || entry.Protocol == meridian.Hysteria2 && !native.hy2 {
			continue
		}
		if entry.Protocol == meridian.VLESSReality && hiddenVLESSCredentials[entry.Material.Credential.ID] {
			continue
		}
		entries = append(entries, entry)
		if entry.Protocol == meridian.VLESSReality {
			vlessBases[entry.Material.Credential.ID] = true
		}
	}
	eligibleRoutes := make([]meridian.PublishedRoute, 0, len(routes))
	for _, route := range routes {
		if vlessBases[route.Grant.Base.ID] {
			eligibleRoutes = append(eligibleRoutes, route)
		}
	}
	value.Entries, value.Routes = entries, eligibleRoutes
	return value, nil
}

// ensureMeridianSubscriptionSnapshotInTx captures the last fully applied
// subscription before desired state is changed. If another change is already
// pending, the prior encrypted snapshot remains authoritative until the Agent
// and publication receipts converge again.
func (s *Store) ensureMeridianSubscriptionSnapshotInTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	var account meridian.Account
	var enabled int
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT id,display_name,total_bytes,expiry_time,reset_days,enabled,desired_revision,applied_revision,status
		FROM meridian_accounts WHERE id=?`, accountID).Scan(&account.ID, &account.DisplayName, &account.Plan.TotalBytes, &account.Plan.ExpiryTime,
		&account.Plan.ResetDays, &enabled, &account.DesiredRevision, &account.AppliedRevision, &status); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: Meridian account was not found")
	} else if err != nil {
		return err
	}
	account.Plan.Enabled = enabled == 1
	if status != "active" || !account.Plan.Enabled || account.AppliedRevision == 0 {
		return nil
	}
	var snapshotRevision sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT account_revision FROM meridian_subscription_snapshots WHERE account_id=?`, accountID).Scan(&snapshotRevision); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if snapshotRevision.Valid && uint64(snapshotRevision.Int64) == account.AppliedRevision {
		return nil
	}
	if account.DesiredRevision != account.AppliedRevision {
		return errors.New("center: Meridian account has no recoverable applied subscription")
	}
	projection, err := s.meridianSubscriptionProjectionInTx(ctx, tx, accountID, false)
	if err != nil {
		var snapshotRevision sql.NullInt64
		if queryErr := tx.QueryRowContext(ctx, `SELECT account_revision FROM meridian_subscription_snapshots WHERE account_id=?`, accountID).Scan(&snapshotRevision); queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		if snapshotRevision.Valid && uint64(snapshotRevision.Int64) == account.AppliedRevision {
			return nil
		}
		if errors.Is(err, errMeridianSubscriptionNotFound) && !snapshotRevision.Valid {
			// There is no available subscription to preserve. Blocking management
			// here would prevent restoring a retired or not-yet-ready first entry.
			// Do not fabricate a snapshot: public rendering remains unavailable
			// until an actual runtime and publication are ready. Other projection
			// errors and existing stale snapshots still fail closed.
			return nil
		}
		return fmt.Errorf("center: capture applied Meridian subscription: %w", err)
	}
	return s.saveMeridianSubscriptionSnapshotInTx(ctx, tx, account, projection)
}

func (s *Store) ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx context.Context, tx *sql.Tx, endpointID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT account_id FROM meridian_credentials WHERE endpoint_id=? ORDER BY account_id`, endpointID)
	if err != nil {
		return err
	}
	accountIDs := []string{}
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, accountID := range accountIDs {
		if err := s.ensureMeridianSubscriptionSnapshotInTx(ctx, tx, accountID); err != nil {
			return err
		}
	}
	return nil
}

// An account change rebuilds the shared endpoint, so subscriptions for every
// account on those endpoints must be captured before either side is changed.
func (s *Store) ensureMeridianSubscriptionSnapshotsForAccountEndpointsInTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT credential.endpoint_id FROM meridian_credentials credential
		JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
		WHERE credential.account_id=? AND endpoint.status<>'retired' ORDER BY credential.endpoint_id`, accountID)
	if err != nil {
		return err
	}
	endpointIDs := []string{}
	for rows.Next() {
		var endpointID string
		if err := rows.Scan(&endpointID); err != nil {
			rows.Close()
			return err
		}
		endpointIDs = append(endpointIDs, endpointID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, endpointID := range endpointIDs {
		if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureAllMeridianSubscriptionSnapshotsInTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM meridian_accounts ORDER BY id`)
	if err != nil {
		return err
	}
	accountIDs := []string{}
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, accountID := range accountIDs {
		if err := s.ensureMeridianSubscriptionSnapshotInTx(ctx, tx, accountID); err != nil {
			return err
		}
	}
	return nil
}
