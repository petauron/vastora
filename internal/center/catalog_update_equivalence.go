package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type updateVersionIdentity struct {
	Version         string `json:"version"`
	PackageRevision int    `json:"packageRevision"`
}

type updateVersionEquivalence struct {
	AppID      string                  `json:"appId"`
	Target     updateVersionIdentity   `json:"target"`
	Equivalent []updateVersionIdentity `json:"equivalent"`
}

// officialUpdateEquivalences accepts only metadata authenticated with the
// current catalog target. Missing, stale, or malformed metadata never hides
// an update and does not change the installed application receipt.
func (s *Store) officialUpdateEquivalences(ctx context.Context) []updateVersionEquivalence {
	state, targetBytes, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || state.Acceptance.Revision == 0 || len(targetBytes) == 0 || s.now().Before(state.Acceptance.ObservedAt) || !s.now().Before(state.Acceptance.ExpiresAt) {
		return nil
	}
	root, err := metadata.Root().FromBytes(state.Metadata["root"])
	if err != nil {
		return nil
	}
	targets, err := metadata.Targets().FromBytes(state.Metadata["targets"])
	if err != nil || targets.Signed.Version < 1 || uint64(targets.Signed.Version) != state.Acceptance.Revision || !s.now().Before(targets.Signed.Expires) || root.VerifyDelegate("targets", targets) != nil {
		return nil
	}
	target := targets.Signed.Targets["stable.json"]
	digest := sha256.Sum256(targetBytes)
	if target == nil || target.Length != int64(len(targetBytes)) || !bytes.Equal(target.Hashes["sha256"], digest[:]) || hex.EncodeToString(digest[:]) != state.Acceptance.SHA256 || target.Custom == nil || len(*target.Custom) > 16384 {
		return nil
	}
	var custom struct {
		VersionEquivalences []updateVersionEquivalence `json:"versionEquivalences"`
	}
	decoder := json.NewDecoder(bytes.NewReader(*target.Custom))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&custom) != nil || decoder.Decode(new(any)) != io.EOF || len(custom.VersionEquivalences) > 32 {
		return nil
	}
	return custom.VersionEquivalences
}

func equivalentUpdate(entries []updateVersionEquivalence, appKey string, installed, available updateVersionIdentity) bool {
	if !strings.HasPrefix(appKey, OfficialCatalogSourceID+"/") || installed.Version == "" || installed.PackageRevision < 1 {
		return false
	}
	appID := strings.TrimPrefix(appKey, OfficialCatalogSourceID+"/")
	for _, entry := range entries {
		if entry.AppID != appID || entry.Target != available || len(entry.Equivalent) > 32 {
			continue
		}
		for _, old := range entry.Equivalent {
			if old == installed {
				return true
			}
		}
	}
	return false
}
