package center

import (
	"context"
	"errors"
	"testing"
)

func TestMeridianRecoveryWithoutAvailableSubscriptionDoesNotInventSnapshot(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	created, err := store.CreateMeridianAccount(ctx, MeridianAccountInput{DisplayName: "Waiting for first entry"})
	if err != nil {
		t.Fatal(err)
	}
	accountID := created.Account.ID
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET applied_revision=desired_revision WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := store.ensureMeridianSubscriptionSnapshotInTx(ctx, tx, accountID); err != nil {
		t.Fatalf("unavailable first entry blocked recovery: %v", err)
	}
	var snapshots int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_subscription_snapshots WHERE account_id=?`, accountID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("fabricated snapshots=%d err=%v", snapshots, err)
	}
	if _, err := store.meridianSubscriptionProjectionInTx(ctx, tx, accountID, false); !errors.Is(err, errMeridianSubscriptionNotFound) {
		t.Fatalf("unavailable entry became publishable: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET desired_revision=desired_revision+1 WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureMeridianSubscriptionSnapshotInTx(ctx, tx, accountID); err == nil {
		t.Fatal("unapplied account without recovery evidence was accepted")
	}
}
