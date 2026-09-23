package center

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petauron/meridian"
)

func TestMeridianRecoveryWithoutAvailableSubscriptionDoesNotInventSnapshot(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Reconstruct an applied account whose entries are no longer available.
	const accountID = "snapshot-recovery-account"
	secretID, err := store.putSecret(ctx, tx, []byte("snapshot-recovery-token"), meridianAccountSecretContext(accountID))
	if err != nil {
		t.Fatal(err)
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES(?, 'Waiting for restored entry', 1, ?, ?, 1, 1, 'active', ?, ?)`, accountID, secretID, meridian.SubscriptionTokenFingerprint("snapshot-recovery-token"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
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
