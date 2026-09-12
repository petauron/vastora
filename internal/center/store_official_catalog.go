package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/catalog"
)

func (s *Store) ConfigureOfficialCatalog(ctx context.Context, origin string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at)
		VALUES(?, 'Vastora Official', ?, ?, 1, 3600, ?) ON CONFLICT(id) DO UPDATE SET url=excluded.url, public_key=excluded.public_key, bearer_secret_id=NULL, custom_ca=NULL, enabled=1`, OfficialCatalogSourceID, origin, []byte{}, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

// readAcceptedOfficialCatalog permits expired display but never accepts a
// cache whose bytes differ from the transactionally recorded target hash.
func readAcceptedOfficialCatalog(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, channel string) (catalog.Catalog, catalog.OfficialAcceptance, error) {
	state, raw, err := readOfficialCatalogTrust(ctx, db, channel)
	if err != nil {
		return catalog.Catalog{}, state.Acceptance, err
	}
	if state.Acceptance.Revision == 0 {
		return catalog.Catalog{}, state.Acceptance, errors.New("center: no trusted official catalog available")
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != state.Acceptance.SHA256 {
		return catalog.Catalog{}, state.Acceptance, errors.New("center: official cache integrity mismatch")
	}
	var target catalog.OfficialTarget
	if err := json.Unmarshal(raw, &target); err != nil {
		return catalog.Catalog{}, state.Acceptance, err
	}
	if target.Source != catalog.OfficialSourceIdentity || target.Channel != channel || target.Revision != state.Acceptance.Revision {
		return catalog.Catalog{}, state.Acceptance, errors.New("center: official cache identity mismatch")
	}
	value, err := catalog.ParseCatalog(target.Catalog)
	return value, state.Acceptance, err
}

// authorizeOfficialManifest must run inside the task-creation transaction.
// Recovery/configuration/uninstall use the installed record, not this gate.
func authorizeOfficialManifest(ctx context.Context, tx *sql.Tx, channel string, manifest catalog.AppManifest, now time.Time) error {
	value, acceptance, err := readAcceptedOfficialCatalog(ctx, tx, channel)
	if err != nil {
		return err
	}
	if now.Before(acceptance.ObservedAt) || !now.Before(acceptance.ExpiresAt) {
		return errors.New("center: refresh the official catalog before installing or upgrading")
	}
	if err := agent.ValidateOfficialContract(manifest); err != nil {
		return err
	}
	expected, err := catalog.CanonicalAppManifest(manifest)
	if err != nil {
		return err
	}
	expectedBytes, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	for _, app := range value.Apps {
		if app.ID != manifest.ID || app.Version != manifest.Version {
			continue
		}
		canonical, err := catalog.CanonicalAppManifest(app)
		if err != nil {
			return err
		}
		actual, err := json.Marshal(canonical)
		if err != nil {
			return err
		}
		if string(actual) == string(expectedBytes) {
			return nil
		}
	}
	return errors.New("center: official catalog changed; select the application again")
}

// RefreshTrustedOfficialCatalog replaces accepted content only after the full
// TUF and executor checks succeed. The root is supplied by program packaging,
// never by the editable source record or by a fetched catalog.
func (s *Store) RefreshTrustedOfficialCatalog(ctx context.Context, origin, channel string, root []byte) (int, error) {
	previous, _, err := s.OfficialCatalogTrust(ctx, channel)
	if err != nil {
		return 0, err
	}
	result, err := catalog.FetchOfficial(ctx, origin, channel, root, previous)
	if err == nil {
		err = s.acceptOfficialCatalog(ctx, origin, result, previous.Acceptance)
	}
	if err != nil {
		if result.Checkpoint != nil {
			// Keep accepted target/history unchanged, but never forget a root
			// revocation or authenticated TUF high-water mark already observed.
			checkpointContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = errors.Join(err, s.preserveOfficialTrustCheckpoint(checkpointContext, channel, previous, *result.Checkpoint))
			cancel()
		}
		return 0, err
	}
	return len(result.Catalog.Apps), nil
}

func (s *Store) acceptOfficialCatalog(ctx context.Context, origin string, result catalog.OfficialFetchResult, previous catalog.OfficialAcceptance) error {
	if err := agent.ValidateOfficialCatalog(result.Catalog); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at, last_checked_at, last_error)
		VALUES(?, 'Vastora Official', ?, ?, 1, 3600, ?, ?, '') ON CONFLICT(id) DO UPDATE SET url=excluded.url, public_key=excluded.public_key, bearer_secret_id=NULL, custom_ca=NULL, enabled=1, last_checked_at=excluded.last_checked_at, last_error=''`, OfficialCatalogSourceID, origin, []byte{}, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if err := commitOfficialCatalogTrust(ctx, tx, result, previous, now); err != nil {
		return err
	}
	return tx.Commit()
}

// A checkpoint comes only from go-tuf's authenticated local cache. Revision
// zero means trust was bootstrapped but no catalog is available to authorize.
func (s *Store) preserveOfficialTrustCheckpoint(ctx context.Context, channel string, previous catalog.OfficialFetchState, checkpoint catalog.OfficialTrustCheckpoint) error {
	if len(checkpoint.Metadata["root"]) == 0 || checkpoint.ObservedAt.IsZero() || checkpoint.ObservedAt.Before(previous.Acceptance.ObservedAt) {
		return errors.New("center: invalid verified catalog checkpoint")
	}
	if reflect.DeepEqual(previous.Metadata, checkpoint.Metadata) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, _, err := readOfficialCatalogTrust(ctx, tx, channel)
	if err != nil {
		return err
	}
	if current.Acceptance != previous.Acceptance || !reflect.DeepEqual(current.Metadata, previous.Metadata) {
		return errors.New("center: official catalog trust changed during refresh")
	}
	encoded, err := json.Marshal(checkpoint.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO official_catalog_trust(channel, revision, target_sha256, observed_at, expires_at, metadata_json, target)
		VALUES(?, 0, '', ?, ?, ?, ?) ON CONFLICT(channel) DO UPDATE SET metadata_json=excluded.metadata_json, observed_at=excluded.observed_at`, channel, checkpoint.ObservedAt.Format(time.RFC3339Nano), time.Time{}.Format(time.RFC3339Nano), encoded, []byte{})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OfficialCatalogTrust(ctx context.Context, channel string) (catalog.OfficialFetchState, []byte, error) {
	return readOfficialCatalogTrust(ctx, s.db, channel)
}

func readOfficialCatalogTrust(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, channel string) (catalog.OfficialFetchState, []byte, error) {
	var state catalog.OfficialFetchState
	var metadata, target []byte
	var observed, expires string
	err := db.QueryRowContext(ctx, `SELECT revision, target_sha256, observed_at, expires_at, metadata_json, target FROM official_catalog_trust WHERE channel = ?`, channel).Scan(&state.Acceptance.Revision, &state.Acceptance.SHA256, &observed, &expires, &metadata, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, nil
	}
	if err != nil {
		return state, nil, err
	}
	state.Acceptance.Channel = channel
	state.Acceptance.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		return state, nil, err
	}
	state.Acceptance.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return state, nil, err
	}
	if err := json.Unmarshal(metadata, &state.Metadata); err != nil {
		return state, nil, err
	}
	return state, target, nil
}

// commitOfficialCatalogTrust is called inside the catalog acceptance transaction
// after TUF verification. A concurrent refresh cannot overwrite a newer result.
func commitOfficialCatalogTrust(ctx context.Context, tx *sql.Tx, result catalog.OfficialFetchResult, expected catalog.OfficialAcceptance, now time.Time) error {
	channel := result.State.Acceptance.Channel
	current, _, err := readOfficialCatalogTrust(ctx, tx, channel)
	if err != nil {
		return err
	}
	if current.Acceptance != expected {
		return errors.New("center: official catalog trust changed during refresh")
	}
	value, accepted, err := catalog.ValidateOfficialTarget(result.Target, channel, current.Acceptance, now)
	if err != nil {
		return err
	}
	// TUF metadata can expire earlier than the target's own signed lifetime.
	if result.State.Acceptance.ExpiresAt.Before(accepted.ExpiresAt) {
		accepted.ExpiresAt = result.State.Acceptance.ExpiresAt
	}
	if !accepted.ExpiresAt.After(now) {
		return errors.New("center: verified official catalog expired before commit")
	}
	for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
		if len(result.State.Metadata[role]) == 0 {
			return fmt.Errorf("center: missing verified %s metadata", role)
		}
	}
	metadata, err := json.Marshal(result.State.Metadata)
	if err != nil {
		return err
	}
	if err := recordCatalogManifestHistory(ctx, tx, OfficialCatalogSourceID, value, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO official_catalog_trust(channel, revision, target_sha256, observed_at, expires_at, metadata_json, target)
		VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(channel) DO UPDATE SET revision=excluded.revision, target_sha256=excluded.target_sha256, observed_at=excluded.observed_at, expires_at=excluded.expires_at, metadata_json=excluded.metadata_json, target=excluded.target`, channel, accepted.Revision, accepted.SHA256, accepted.ObservedAt.Format(time.RFC3339Nano), accepted.ExpiresAt.Format(time.RFC3339Nano), metadata, result.Target)
	return err
}
