package center

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompositeMigrationPreservesIdentitiesUsageAndPreMigrationBackup(t *testing.T) {
	directory := t.TempDir()
	old := meridianTrafficVersion107Fixture(t, directory)
	provider, err := newMigrationProvider(old.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 110); err != nil {
		t.Fatal(err)
	}
	before := meridianVersion88PreservedState(t, old.db)
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if !reflect.DeepEqual(before, meridianVersion88PreservedState(t, migrated.db)) {
		t.Fatal("composite migration changed existing credentials, route identities, snapshots or quota watermarks")
	}
	var secondary int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM meridian_route_grants WHERE ipv4_egress_node_id IS NOT NULL`).Scan(&secondary); err != nil || secondary != 0 {
		t.Fatal("migration invented composite exits")
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v110-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatal("pre-migration backup absent")
	}
	var index string
	if err := migrated.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='meridian_route_grants_identity'`).Scan(&index); err != nil {
		t.Fatal(err)
	}
}
