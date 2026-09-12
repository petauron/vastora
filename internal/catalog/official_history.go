package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// OfficialManifestHistory is retained by the protected publication ledger, not
// reconstructed from whatever entries the CDN currently serves. Removed app
// versions remain here and cannot be reintroduced with different content.
type OfficialManifestHistory map[string]map[string]string

func ExtendOfficialManifestHistory(previous OfficialManifestHistory, value Catalog) (OfficialManifestHistory, error) {
	result := make(OfficialManifestHistory)
	for id, versions := range previous {
		result[id] = make(map[string]string)
		for version, digest := range versions {
			raw, err := hex.DecodeString(digest)
			if err != nil || len(raw) != sha256.Size {
				return nil, fmt.Errorf("catalog: invalid protected manifest history")
			}
			result[id][version] = digest
		}
	}
	for _, app := range value.Apps {
		canonical, err := CanonicalAppManifest(app)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(canonical)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		hash := hex.EncodeToString(digest[:])
		if result[app.ID] == nil {
			result[app.ID] = make(map[string]string)
		}
		if existing := result[app.ID][app.Version]; existing != "" && existing != hash {
			return nil, fmt.Errorf("catalog: published content changed for %s@%s", app.ID, app.Version)
		}
		result[app.ID][app.Version] = hash
	}
	return result, nil
}
