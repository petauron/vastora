package center

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/petauron/meridian"
)

func TestVersion108PreservesTotalsWithoutInventingDirectionalHistory(t *testing.T) {
	dir := t.TempDir()
	old := legacyMigrationStore(t, dir, 107)
	seedMeridianVersion88HealthFixture(t, old.db)
	before := meridianVersion88PreservedState(t, old.db)
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !reflect.DeepEqual(before, meridianVersion88PreservedState(t, store.db)) {
		t.Fatal("migration changed existing identities or usage")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_line_usage`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invented history: %d %v", count, err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "migration-backups", fmt.Sprintf("center-v107-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing backup: %v %v", backups, err)
	}
}

func TestMeridianTrafficAggregatesDirectionsWithoutRecountingOrQuotaMutation(t *testing.T) {
	dir := t.TempDir()
	old := legacyMigrationStore(t, dir, 107)
	seedMeridianVersion88HealthFixture(t, old.db)
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	view, err := store.MeridianTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Lines) != 2 {
		t.Fatalf("want native and landing, got %+v", view)
	}
	for _, line := range view.Lines {
		if line.State != "missing" {
			t.Fatal("historical raw counters became usage")
		}
	}
	snapshots := []meridian.CounterSnapshot{}
	for _, state := range []string{"ready", "pending", "revoking", "revoked", "failed"} {
		for _, kind := range []string{"native", "route"} {
			snapshots = append(snapshots, meridian.CounterSnapshot{CredentialID: "v88-health-account-" + state + "-" + kind, UpBytes: 200, DownBytes: 250})
		}
	}
	observe := func(values []meridian.CounterSnapshot, rollback bool) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := store.observeMeridianUsageInTx(ctx, tx, "v88-health-endpoint", values, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if !rollback {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	observe(snapshots, false)
	for i := range snapshots {
		if i%2 == 0 {
			snapshots[i].UpBytes += 10
			snapshots[i].DownBytes += 20
		} else {
			snapshots[i].UpBytes += 30
			snapshots[i].DownBytes += 40
		}
	}
	now = now.Add(time.Minute)
	observe(snapshots, false)
	observe(snapshots, false)
	view, err = store.MeridianTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range view.Lines {
		up, down := int64(50), int64(100)
		if line.EgressNodeID != "" {
			up, down = 150, 200
		}
		if line.UploadBytes != up || line.DownloadBytes != down || line.TotalBytes != up+down || line.TrackedCredentials != 5 || line.State != "current" {
			t.Fatalf("wrong aggregation: %+v", line)
		}
	}
	snapshots[0].UpBytes = 3
	snapshots[0].DownBytes = 4
	now = now.Add(time.Minute)
	observe(snapshots[:1], true)
	var up, down int64
	if err := store.db.QueryRow(`SELECT upload_bytes,download_bytes FROM meridian_line_usage WHERE credential_id=?`, snapshots[0].CredentialID).Scan(&up, &down); err != nil || up != 10 || down != 20 {
		t.Fatalf("rollback leaked: %d/%d %v", up, down, err)
	}
	observe(snapshots[:1], false)
	observe(snapshots[:1], false)
	if err := store.db.QueryRow(`SELECT upload_bytes,download_bytes FROM meridian_line_usage WHERE credential_id=?`, snapshots[0].CredentialID).Scan(&up, &down); err != nil || up != 13 || down != 24 {
		t.Fatalf("reset/duplicate: %d/%d %v", up, down, err)
	}
	var beforeRevision, afterRevision int64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='v88-health-endpoint'`).Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	view, err = store.MeridianTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range view.Lines {
		if line.State != "stale" {
			t.Fatalf("old sample shown current: %+v", line)
		}
	}
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='v88-health-endpoint'`).Scan(&afterRevision); err != nil || beforeRevision != afterRevision {
		t.Fatal("read changed runtime revision")
	}
	if _, err := store.db.Exec(`DELETE FROM meridian_line_usage WHERE credential_id=?`, snapshots[0].CredentialID); err != nil {
		t.Fatal(err)
	}
	view, err = store.MeridianTraffic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Lines[0].State != "partial" {
		t.Fatalf("missing credential hidden: %+v", view)
	}
}
