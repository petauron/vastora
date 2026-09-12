package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/catalog"
	"time"
)

// SeedOfficialCatalog injects already-verified content for store tests only.
// Production has no local signer or seed path. Cryptographic verification is
// covered separately by the TUF repository tests.
func (s *Store) SeedOfficialCatalog(ctx context.Context, payload []byte) error {
	value, err := catalog.ParseCatalog(payload)
	if err != nil {
		return err
	}
	if err := s.ConfigureOfficialCatalog(ctx, "https://example.invalid/catalog"); err != nil {
		return err
	}
	previous, _, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	raw, err := json.Marshal(catalog.OfficialTarget{Source: catalog.OfficialSourceIdentity, Channel: "stable", Revision: previous.Acceptance.Revision + 1, GeneratedAt: now, ExpiresAt: now.Add(24 * time.Hour), Catalog: payload})
	if err != nil {
		return err
	}
	_, acceptance, err := catalog.ValidateOfficialTarget(raw, "stable", previous.Acceptance, now)
	if err != nil {
		return err
	}
	result := catalog.OfficialFetchResult{Catalog: value, Target: raw, State: catalog.OfficialFetchState{Acceptance: acceptance, Metadata: map[string][]byte{"root": []byte("fixture"), "timestamp": []byte("fixture"), "snapshot": []byte("fixture"), "targets": []byte("fixture")}}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := commitOfficialCatalogTrust(ctx, tx, result, previous.Acceptance, now); err != nil {
		return err
	}
	return tx.Commit()
}
