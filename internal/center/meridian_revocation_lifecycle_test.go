package center

import (
	"context"
	"testing"
	"time"
)

func TestMeridianRouteRevocationSurvivesAccountInvalidations(t *testing.T) {
	for _, operation := range []string{"quota-boundary", "scheduled-reset", "account-expiry", "account-update"} {
		t.Run(operation, func(t *testing.T) {
			store, projection, commandID, grantID := openMeridianHealthCompletionFixture(t)
			ctx := context.Background()
			completeMeridianHealthFixture(t, store, projection, commandID, meridianHealthResult(projection, store.now(), true))
			if err := store.RevokeMeridianRouteGrant(ctx, grantID); err != nil {
				t.Fatal(err)
			}

			if operation == "account-update" {
				enabled := true
				if _, err := store.UpdateMeridianAccount(ctx, sharedSnapshotAccountA, MeridianAccountInput{
					DisplayName: "Updated account", TotalBytes: 2000, Enabled: &enabled,
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				stamp := store.now().UTC().Format(time.RFC3339Nano)
				past := store.now().UTC().Add(-time.Minute)
				switch operation {
				case "quota-boundary":
					_, err = tx.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET observed_bytes=1000
						WHERE credential_id=?`, sharedSnapshotAccountA+"-native")
					if err == nil {
						err = store.markMeridianQuotaBoundaryChanged(ctx, tx, []string{sharedSnapshotAccountA}, stamp)
					}
				case "scheduled-reset":
					_, err = tx.ExecContext(ctx, `UPDATE meridian_accounts SET reset_days=1,next_reset_at=? WHERE id=?`, past.Format(time.RFC3339Nano), sharedSnapshotAccountA)
					if err == nil {
						err = store.resetDueMeridianAccounts(ctx, tx)
					}
				case "account-expiry":
					_, err = tx.ExecContext(ctx, `UPDATE meridian_accounts SET expiry_time=? WHERE id=?`, past.UnixMilli(), sharedSnapshotAccountA)
					if err == nil {
						err = store.resetDueMeridianAccounts(ctx, tx)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}

			checkRevocation := func(wantStatus string, applied bool) {
				t.Helper()
				var status string
				var enabled, credentialEnabled, healthy int
				var desired, observed, expires int64
				if err := store.db.QueryRowContext(ctx, `SELECT grant_row.status,grant_row.enabled,credential.enabled,
					grant_row.runtime_healthy,grant_row.desired_revision,grant_row.applied_revision,grant_row.health_expires_unix_ms
					FROM meridian_route_grants grant_row JOIN meridian_credentials credential ON credential.id=grant_row.route_credential_id
					WHERE grant_row.id=?`, grantID).Scan(&status, &enabled, &credentialEnabled, &healthy, &desired, &observed, &expires); err != nil {
					t.Fatal(err)
				}
				if status != wantStatus || enabled != 0 || credentialEnabled != 0 || healthy != 0 || (desired == observed) != applied {
					t.Fatalf("revocation changed: status=%s enabled=%d credential=%d healthy=%d desired=%d applied=%d", status, enabled, credentialEnabled, healthy, desired, observed)
				}
				if applied && expires != 0 {
					t.Fatal("removed route retained a transport lease")
				}
			}
			checkRevocation("revoking", false)

			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			nextCommandID, err := store.queueMeridianRuntime(ctx, tx, sharedSnapshotEndpointID, false)
			if err != nil {
				t.Fatal(err)
			}
			next, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(next.task.Peers) != 0 || next.task.Source != nil {
				t.Fatal("revoked route remained in the replacement transport contract")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, nextCommandID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			completeMeridianHealthFixture(t, store, next, nextCommandID, meridianHealthResult(next, store.now(), false))
			checkRevocation("revoked", true)
			if other, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil || len(other.Entries) != 1 {
				t.Fatalf("revocation interrupted another account: entries=%d err=%v", len(other.Entries), err)
			}
		})
	}
}
