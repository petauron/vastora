package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func (s *Store) OfficialUIAsset(ctx context.Context, appID, version, kind string) ([]byte, error) {
	var name string
	var err error
	switch kind {
	case "script":
		name, err = catalog.OfficialUITargetName(appID, version)
	case "style":
		name, err = catalog.OfficialUIStylesheetTargetName(appID, version)
	default:
		return nil, errors.New("center: unsupported application UI asset")
	}
	if err != nil {
		return nil, err
	}
	var bundle []byte
	var expected string
	var trustedMetadata []byte
	err = s.db.QueryRowContext(ctx, `SELECT asset.bundle, asset.sha256, trust.metadata_json
		FROM official_app_ui_assets asset
		JOIN official_catalog_trust trust ON trust.channel='stable' AND trust.revision=asset.catalog_revision
		WHERE asset.app_id=? AND asset.asset_kind=? AND asset.app_version=? AND asset.target_name=?`, appID, kind, version, name).Scan(&bundle, &expected, &trustedMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(bundle)
	if len(bundle) == 0 || len(bundle) > catalog.MaxOfficialUIBytes || hex.EncodeToString(hash[:]) != expected {
		return nil, errors.New("center: official application UI cache integrity mismatch")
	}
	// The row's own hash is not a trust anchor; bind it to the accepted TUF target.
	var roles map[string][]byte
	if err := json.Unmarshal(trustedMetadata, &roles); err != nil {
		return nil, errors.New("center: official application UI trust metadata is invalid")
	}
	targets, err := metadata.Targets().FromBytes(roles["targets"])
	if err != nil {
		return nil, errors.New("center: official application UI target metadata is invalid")
	}
	target, ok := targets.Signed.Targets[name]
	if !ok || target == nil || target.Length != int64(len(bundle)) || !bytes.Equal(target.Hashes["sha256"], hash[:]) {
		return nil, errors.New("center: official application UI differs from signed target")
	}
	return bundle, nil
}
