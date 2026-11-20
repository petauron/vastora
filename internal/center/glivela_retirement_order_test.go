package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/glivelaruntime"
)

func TestGlivelaControllerRetirementWaitsForEveryLegacyEntry(t *testing.T) {
	store, _, clock := openGlivelaCutoverHealthFixture(t, "retire")
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, "UPDATE meridian_endpoints SET status='failed' WHERE id=?", sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := store.queueMissingGlivelaRetirements(ctx, tx, clock); err != nil {
		t.Fatal(err)
	}
	var controllerRetirements int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM application_commands WHERE kind=? AND state='pending'", glivelaruntime.LegacyRetireKind).Scan(&controllerRetirements); err != nil {
		t.Fatal(err)
	}
	if controllerRetirements != 0 {
		t.Fatal("subscription host retired before the unretired entry was reconciled")
	}
}
