package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestMeridianCutoverVerificationRequiresUnexpiredLandingEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		delta time.Duration
		fresh bool
	}{
		{name: "expired", delta: -time.Millisecond},
		{name: "expires exactly now"},
		{name: "fresh", delta: landing.AllowLifetime, fresh: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, grantID, clock := openMeridianCutoverHealthFixture(t, "verify")
			ctx := context.Background()
			if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET health_expires_unix_ms=? WHERE id=?`, clock.Add(test.delta).UnixMilli(), grantID); err != nil {
				t.Fatal(err)
			}
			view, err := store.MeridianCutover(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantReady, wantBlocked, wantState := 0, 1, "verify"
			if test.fresh {
				wantReady, wantBlocked, wantState = 1, 0, "retire"
			}
			if view.ReadyRoutes != wantReady || view.BlockedRoutes != wantBlocked || view.ReadyEndpoints != 1 {
				t.Fatalf("health summary ready=%d blocked=%d endpoints=%d", view.ReadyRoutes, view.BlockedRoutes, view.ReadyEndpoints)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.reconcileMeridianCutoverInTx(ctx, tx, clock.Format(time.RFC3339Nano)); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			view, err = store.MeridianCutover(ctx)
			if err != nil || view.State != wantState {
				t.Fatalf("verification state=%s want=%s err=%v", view.State, wantState, err)
			}
			// A landing receipt expiring does not invalidate the native entry or
			// the unrelated account's original subscription and saved snapshot.
			if native, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil || len(native.Entries) != 1 {
				t.Fatalf("unrelated native subscription was hidden: entries=%d err=%v", len(native.Entries), err)
			}
			var snapshots int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_subscription_snapshots WHERE account_id=?`, sharedSnapshotAccountB).Scan(&snapshots); err != nil || snapshots != 1 {
				t.Fatalf("unrelated applied snapshot was removed: count=%d err=%v", snapshots, err)
			}
		})
	}
}

func TestMeridianRetirementDoesNotCompleteAfterLandingEvidenceExpires(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "expired"
		if fresh {
			name = "fresh"
		}
		t.Run(name, func(t *testing.T) {
			store, grantID, clock := openMeridianCutoverHealthFixture(t, "retire")
			ctx := context.Background()
			expires := clock.UnixMilli()
			if fresh {
				expires = clock.Add(landing.AllowLifetime).UnixMilli()
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET health_expires_unix_ms=? WHERE id=?`, expires, grantID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET legacy_retired=1 WHERE id=?`, sharedSnapshotEndpointID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
				SELECT 'health-controller-retired',application.id,application.node_id,application.node_id,?,'{}','succeeded',?,?
				FROM applications application WHERE application.id='cutover-health-controller'`, meridianruntime.LegacyRetireKind, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.reconcileMeridianCutoverInTx(ctx, tx, clock.Format(time.RFC3339Nano)); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			wantState := "retire"
			if fresh {
				wantState = "complete"
			}
			view, err := store.MeridianCutover(ctx)
			if err != nil || view.State != wantState || view.Complete != fresh {
				t.Fatalf("retirement state=%s complete=%t want=%s err=%v", view.State, view.Complete, wantState, err)
			}
		})
	}
}

func TestExplicitMeridianRetirementRetryCannotBypassExpiredLandingEvidence(t *testing.T) {
	store, grantID, clock := openMeridianCutoverHealthFixture(t, "retire")
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET legacy_retired=1 WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	queue := func(expires int64) int {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET health_expires_unix_ms=? WHERE id=?`, expires, grantID); err != nil {
			t.Fatal(err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.queueMissingMeridianRetirements(ctx, tx, clock); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var pending int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE kind=? AND state='pending'`, meridianruntime.LegacyRetireKind).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	}
	if pending := queue(clock.UnixMilli()); pending != 0 {
		t.Fatalf("expired landing evidence queued %d controller retirements", pending)
	}
	if pending := queue(clock.Add(landing.AllowLifetime).UnixMilli()); pending != 1 {
		t.Fatalf("fresh landing evidence did not allow controller retirement: pending=%d", pending)
	}
}

func openMeridianCutoverHealthFixture(t *testing.T, phase string) (*Store, string, time.Time) {
	t.Helper()
	store, _, grantID, _ := openMeridianRuntimeIdentityFixture(t)
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	stamp := clock.Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready',health_expires_unix_ms=? WHERE id=?`, clock.Add(landing.AllowLifetime).UnixMilli(), grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		SELECT 'cutover-health-controller','Subscription host',node_id,site_id,?,'','running','docker','',?,?
		FROM applications WHERE id='snapshot-shared-app'`, meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,management,status,created_at,updated_at)
		SELECT 'cutover-health-subscription',id,site_id,?,'http',8080,8080,?,'system',?,0,'ready',?,?
		FROM applications WHERE id='cutover-health-controller'`, meridianSubscriptionServiceName, dockerruntime.CenterAlias+":8080", meridianSubscriptionProtocol, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		SELECT 'cutover-health-subscription-publication','cutover-health-subscription','public_direct','application_node',node_id,'subscription.example.test','manual',1,1,1,'ready',?,?
		FROM applications WHERE id='cutover-health-controller'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state=?,subscription_authority='meridian',legacy_controller_application_id='cutover-health-controller',expected_accounts=2,expected_credentials=3,expected_endpoints=1,expected_routes=1,last_error='',updated_at=? WHERE id=1`, phase, stamp); err != nil {
		t.Fatal(err)
	}
	return store, grantID, clock
}
