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

// filterMeridianSubscriptionSnapshotRoutesInTx keeps the last applied native
// inventory available while refusing to re-publish a fixed route whose landing
// runtime is no longer live. A hidden native entry remains hidden when its
// route is withdrawn, so a fixed egress can fail closed without silently
// becoming a direct connection.
func (s *Store) filterMeridianSubscriptionSnapshotRoutesInTx(ctx context.Context, tx *sql.Tx, value meridianAppliedSubscriptionSnapshot) (meridianAppliedSubscriptionSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.egress_node_id,grant_row.base_credential_id,grant_row.hide_native,grant_row.enabled,grant_row.status,
		CASE WHEN grant_row.enabled=1 AND grant_row.status NOT IN ('blocked','revoking','revoked')
		 AND landing.status='ready' AND landing.desired_revision=landing.applied_revision
		 AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?
		 THEN 1 ELSE 0 END
		FROM meridian_route_grants grant_row
		LEFT JOIN landing_server_states landing ON landing.node_id=grant_row.egress_node_id
		LEFT JOIN agents agent ON agent.id=grant_row.egress_node_id
		WHERE grant_row.account_id=? ORDER BY grant_row.id`, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), value.AccountID)
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
	for _, entry := range value.Entries {
		if entry.Protocol == meridian.VLESSReality && hiddenVLESSCredentials[entry.Material.Credential.ID] {
			continue
		}
		entries = append(entries, entry)
	}
	value.Entries, value.Routes = entries, routes
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
