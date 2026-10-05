package center

import (
	"context"
	"testing"
	"time"
)

func TestMeridianRouteRecreationRequiresCompletedRevocation(t *testing.T) {
	store, grantID, _ := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	var input MeridianRouteGrantInput
	var oldCredential string
	if err := store.db.QueryRow(`SELECT account_id,endpoint_id,egress_node_id,route_credential_id FROM meridian_route_grants WHERE id=?`, grantID).Scan(&input.AccountID, &input.EndpointID, &input.EgressNodeID, &oldCredential); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMeridianRouteGrant(ctx, input); err == nil {
		t.Fatal("duplicate active route accepted")
	}
	if err := store.RevokeMeridianRouteGrant(ctx, grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMeridianRouteGrant(ctx, input); err == nil {
		t.Fatal("unfinished revocation accepted")
	}
	// Model the Agent's completed removal receipt, retaining the revoked row.
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET status='revoked',applied_revision=desired_revision,health_expires_unix_ms=0 WHERE id=?`, grantID); err != nil {
		t.Fatal(err)
	}
	var previousRevision int64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_route_grants WHERE id=?`, grantID).Scan(&previousRevision); err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateMeridianRouteGrant(ctx, input)
	if err != nil || grant.ID != grantID {
		t.Fatalf("recreate revoked route: %v", err)
	}
	var credential, status string
	var revision, healthy, expires, oldEnabled int64
	if err := store.db.QueryRow(`SELECT route_credential_id,status,desired_revision,runtime_healthy,health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, grantID).Scan(&credential, &status, &revision, &healthy, &expires); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT enabled FROM meridian_credentials WHERE id=?`, oldCredential).Scan(&oldEnabled); err != nil {
		t.Fatal(err)
	}
	if credential == oldCredential || status != "pending" || revision <= previousRevision || healthy != 0 || expires != 0 || oldEnabled != 0 {
		t.Fatal("recreated route reused revoked authority or stale health")
	}
}

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
