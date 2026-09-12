package center

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

func TestOfficialTrustTransactionAndReplayState(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := s.now().UTC()
	payload, err := os.ReadFile("../catalog/testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	target := catalog.OfficialTarget{Source: catalog.OfficialSourceIdentity, Channel: "stable", Revision: 1, GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Catalog: payload}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	value, acceptance, err := catalog.ValidateOfficialTarget(raw, "stable", catalog.OfficialAcceptance{}, now)
	if err != nil {
		t.Fatal(err)
	}
	// This store test exercises post-verification persistence, not TUF signatures.
	result := catalog.OfficialFetchResult{Catalog: value, Target: raw, State: catalog.OfficialFetchState{Acceptance: acceptance, Metadata: map[string][]byte{"root": []byte("verified-root"), "timestamp": []byte("verified-timestamp"), "snapshot": []byte("verified-snapshot"), "targets": []byte("verified-targets")}}}
	// Manifest history is scoped to an existing source. Its identity is retained
	// independently from the new non-deletable trust state.
	_, err = s.db.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at) VALUES(?, 'Official', 'https://example.invalid/catalog', ?, 1, 3600, ?)`, OfficialCatalogSourceID, make([]byte, 32), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitOfficialCatalogTrust(ctx, tx, result, catalog.OfficialAcceptance{}, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	state, _, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || state.Acceptance.Revision != 0 {
		t.Fatalf("rollback leaked state: %+v %v", state, err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitOfficialCatalogTrust(ctx, tx, result, catalog.OfficialAcceptance{}, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state, cached, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || string(cached) != string(raw) || state.Acceptance.Revision != 1 {
		t.Fatalf("state missing: %+v %v", state, err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitOfficialCatalogTrust(ctx, tx, result, catalog.OfficialAcceptance{}, now); err == nil {
		tx.Rollback()
		t.Fatal("stale refresh accepted")
	}
	tx.Rollback()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM catalog_sources WHERE id = ?`, OfficialCatalogSourceID); err != nil {
		t.Fatal(err)
	}
	state, _, err = s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || state.Acceptance.Revision != 1 {
		t.Fatalf("source deletion erased trust: %+v %v", state, err)
	}
	if _, _, err := readAcceptedOfficialCatalog(ctx, s.db, "stable"); err != nil {
		t.Fatalf("accepted cache is not readable: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE official_catalog_trust SET expires_at = ? WHERE channel = 'stable'`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAcceptedOfficialCatalog(ctx, s.db, "stable"); err != nil {
		t.Fatalf("expired display was blocked: %v", err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizeOfficialManifest(ctx, tx, "stable", value.Apps[0], now); err == nil {
		tx.Rollback()
		t.Fatal("expired cache authorized installation")
	}
	tx.Rollback()
	if _, err := s.db.ExecContext(ctx, `UPDATE official_catalog_trust SET target = ? WHERE channel = 'stable'`, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAcceptedOfficialCatalog(ctx, s.db, "stable"); err == nil {
		t.Fatal("tampered cache displayed as trusted")
	}
}

func TestOfficialTrustCheckpointSurvivesBootstrapFailureAndRestart(t *testing.T) {
	directory := t.TempDir()
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := s.now().UTC()
	checkpoint := catalog.OfficialTrustCheckpoint{Metadata: map[string][]byte{"root": []byte("verified-new-root")}, ObservedAt: now}
	if err := s.preserveOfficialTrustCheckpoint(ctx, "stable", catalog.OfficialFetchState{}, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAcceptedOfficialCatalog(ctx, s.db, "stable"); err == nil {
		t.Fatal("root-only checkpoint authorized a catalog")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, target, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || state.Acceptance.Revision != 0 || len(target) != 0 || string(state.Metadata["root"]) != "verified-new-root" || !state.Acceptance.ObservedAt.Equal(now) {
		t.Fatalf("checkpoint lost on restart: %+v %v", state, err)
	}
	if err := s.ConfigureOfficialCatalog(ctx, "https://example.invalid/catalog/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM catalog_sources WHERE id = ?`, OfficialCatalogSourceID); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.OfficialCatalogTrust(ctx, "stable")
	if err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("source lifecycle erased trust checkpoint")
	}
	checkpoint.Metadata = map[string][]byte{"root": []byte("another-root")}
	if err := s.preserveOfficialTrustCheckpoint(ctx, "stable", catalog.OfficialFetchState{}, checkpoint); err == nil {
		t.Fatal("stale concurrent checkpoint overwrote newer trust")
	}
}

func TestOfficialCheckpointPreservesAcceptedCatalogAndExpiry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	value, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedOfficialCatalog(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	before, target, err := s.OfficialCatalogTrust(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	metadata := make(map[string][]byte)
	for key, raw := range before.Metadata {
		metadata[key] = raw
	}
	metadata["root"] = []byte("verified-rotated-root")
	checkpoint := catalog.OfficialTrustCheckpoint{Metadata: metadata, ObservedAt: before.Acceptance.ObservedAt.Add(time.Second)}
	if err := s.preserveOfficialTrustCheckpoint(context.Background(), "stable", before, checkpoint); err != nil {
		t.Fatal(err)
	}
	after, cached, err := s.OfficialCatalogTrust(context.Background(), "stable")
	if err != nil || after.Acceptance.Revision != before.Acceptance.Revision || after.Acceptance.SHA256 != before.Acceptance.SHA256 || after.Acceptance.ExpiresAt != before.Acceptance.ExpiresAt || string(target) != string(cached) {
		t.Fatalf("trust checkpoint altered accepted content or expiry: %v", err)
	}
}
