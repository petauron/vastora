package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/networking"
)

func TestIPQualityLatestResultAndStaleAttempt(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "quality-test", NodeCapabilities{Docker: true, IPQuality: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET public_egress_address='203.0.113.8',public_egress_bind_address='10.0.0.18',public_egress_mode='nat',public_egress_observed_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID); err == nil {
		t.Fatal("duplicate diagnostic queued")
	}
	claim := func() *AgentTask {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		task, err := s.claimIPQuality(ctx, tx, node.ID)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	task := claim()
	report := ipquality.Report{Address: "203.0.113.8", Version: "test", Scores: []ipquality.Score{{Source: "IPQS", Value: "75"}}, Services: []ipquality.Service{{Name: "Netflix", Status: "Yes", RegionCode: "CA"}}}
	raw, _ := json.Marshal(map[string]any{"ipQuality": ipquality.Result{Report: &report}})
	if err := s.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt+1, true, "", raw, false); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err := s.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err == nil {
		t.Fatal("old check overwrote current check")
	}
	if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, second.ID, second.Attempt, true, json.RawMessage(`{"ipQuality":{"error":"timeout"}}`)); err != nil {
		t.Fatal(err)
	}
	values, err := s.ListIPQuality(ctx)
	if err != nil || len(values) != 1 || values[0].State != "succeeded" || values[0].Error != "timeout" || values[0].Report == nil || values[0].Report.Scores[0].Value != "75" {
		t.Fatalf("failed diagnostic lost previous report: %#v %v", values, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET public_egress_address='203.0.113.9' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	values, err = s.ListIPQuality(ctx)
	if err != nil || !values[0].Stale {
		t.Fatal("IP change did not invalidate report")
	}
}

func TestIPQualityMigration77AddsEmptyLatestResults(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 76); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 77); err != nil {
		t.Fatal(err)
	}
	var version, count int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 77 {
		t.Fatalf("version: %d %v", version, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM ip_quality_checks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration generated diagnostic tasks: %d %v", count, err)
	}
}
