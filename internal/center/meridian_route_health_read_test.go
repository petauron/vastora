package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/networking"
)

func openMeridianRouteHealthReadFixture(t *testing.T) (*Store, string, time.Time) {
	t.Helper()
	store := openMeridianSharedEndpointSnapshotFixture(t)
	now := store.now().UTC().Truncate(time.Millisecond)
	store.now = func() time.Time { return now }
	egress := enrollOrchestrationNode(t, store, "route-health-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, egress.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,peer_json,status,updated_at)
		VALUES(?,1,1,'{}','{}','ready',?)`, egress.ID, stamp); err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountB, EndpointID: sharedSnapshotEndpointID, EgressNodeID: egress.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready',health_expires_unix_ms=? WHERE id=?`, now.Add(10*time.Second).UnixMilli(), grant.ID); err != nil {
		t.Fatal(err)
	}
	return store, grant.ID, now
}

func TestMeridianRouteHealthExpiresInLiveAndAppliedSnapshotReads(t *testing.T) {
	store, grantID, now := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	initial, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(initial.Entries) != 1 || len(initial.Routes) != 1 {
		t.Fatalf("fresh fixed route: entries=%d routes=%d err=%v", len(initial.Entries), len(initial.Routes), err)
	}
	grants, err := store.listMeridianRouteGrants(ctx)
	if err != nil || len(grants) != 1 || !grants[0].RuntimeHealthy || grants[0].Status != "ready" {
		t.Fatalf("fresh management health=%#v err=%v", grants, err)
	}
	// No Agent report or scheduler mutation arrives. The stored healthy flag
	// remains true as its lease reaches the exact millisecond boundary.
	expired := now.Add(10 * time.Second)
	store.now = func() time.Time { return expired }
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	fallback, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(fallback.Entries) != 1 || len(fallback.Routes) != 0 {
		t.Fatalf("snapshot revived expired peer: entries=%d routes=%d err=%v", len(fallback.Entries), len(fallback.Routes), err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err != nil || len(snapshot.Routes) != 1 {
		t.Fatalf("test must filter a real prior snapshot without rewriting it: routes=%d err=%v", len(snapshot.Routes), err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	live, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(live.Entries) != 1 || len(live.Routes) != 0 {
		t.Fatalf("live projection revived expired peer: entries=%d routes=%d err=%v", len(live.Entries), len(live.Routes), err)
	}
	grants, err = store.listMeridianRouteGrants(ctx)
	if err != nil || len(grants) != 1 || grants[0].RuntimeHealthy || grants[0].Status != "blocked" || grants[0].LastError == "" {
		t.Fatalf("management still presents expired route as healthy: %#v err=%v", grants, err)
	}
	var storedHealthy int
	var storedStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT runtime_healthy,status FROM meridian_route_grants WHERE id=?`, grantID).Scan(&storedHealthy, &storedStatus); err != nil || storedHealthy != 1 || storedStatus != "ready" {
		t.Fatalf("read-time validation unexpectedly mutated durable receipts: healthy=%d status=%q err=%v", storedHealthy, storedStatus, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET health_expires_unix_ms=? WHERE id=?`, expired.Add(10*time.Second).UnixMilli(), grantID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(recovered.Routes) != 1 {
		t.Fatalf("fresh peer renewal did not restore route publication: routes=%d err=%v", len(recovered.Routes), err)
	}
}

func TestMeridianAppliedSnapshotRequiresCurrentRouteEvidenceAndPreservesHiddenNative(t *testing.T) {
	store, grantID, _ := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		assignment string
		hidden     bool
	}{
		{"missing lease", "health_expires_unix_ms=0", false},
		{"blocked with future lease", "status='blocked'", false},
		{"unhealthy with future lease", "runtime_healthy=0", false},
		{"pending with future lease", "status='pending'", false},
		{"unapplied revision", "desired_revision=desired_revision+1", false},
		{"hidden expired route", "health_expires_unix_ms=0,hide_native=1", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET `+test.assignment+` WHERE id=?`, grantID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
			if err != nil || len(snapshot.Routes) != 1 {
				t.Fatalf("prior snapshot: routes=%d err=%v", len(snapshot.Routes), err)
			}
			filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
			wantEntries := 1
			if test.hidden {
				wantEntries = 0
			}
			if err != nil || len(filtered.Routes) != 0 || len(filtered.Entries) != wantEntries {
				t.Fatalf("snapshot did not enforce current route authority: entries=%d routes=%d err=%v", len(filtered.Entries), len(filtered.Routes), err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMeridianImportedRoutesUseLegacySnapshotOnlyUntilFirstAppliedEndpoint(t *testing.T) {
	store, grantID, now := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='project',subscription_authority='meridian',import_sha256=? WHERE id=1`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=0,runtime_healthy=0,status='pending' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=0,runtime_healthy=0,status='pending',health_expires_unix_ms=0 WHERE id=?`, grantID); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(legacy.Entries) != 1 || len(legacy.Routes) != 1 {
		t.Fatalf("pre-switch legacy route disappeared: entries=%d routes=%d err=%v", len(legacy.Entries), len(legacy.Routes), err)
	}
	links, err := meridian.RenderLinks(legacy.Entries, sharedSnapshotAccountB, meridian.FixedMode, legacy.Routes, false)
	if err != nil || strings.Count(string(links), "vless://") != 2 {
		t.Fatalf("imported pending revisions were not rendered as existing legacy links: links=%q err=%v", links, err)
	}
	legacySnapshot := meridianAppliedSubscriptionSnapshot{AccountID: sharedSnapshotAccountB, AccountRevision: legacy.Account.AppliedRevision, Entries: legacy.Entries, Routes: legacy.Routes}
	for _, state := range []struct{ name, query string }{
		{"missing import evidence", `UPDATE meridian_cutover SET import_sha256='' WHERE id=1`},
		{"outside cutover", `UPDATE meridian_cutover SET state='complete' WHERE id=1`},
		{"explicitly blocked legacy route", `UPDATE meridian_route_grants SET status='blocked'`},
	} {
		t.Run(state.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, state.query); err != nil {
				t.Fatal(err)
			}
			filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, legacySnapshot)
			if err != nil || len(filtered.Routes) != 0 || len(filtered.Entries) != 1 {
				t.Fatalf("legacy exception escaped its boundary: entries=%d routes=%d err=%v", len(filtered.Entries), len(filtered.Routes), err)
			}
		})
	}
	// One successful Meridian runtime receipt permanently ends this endpoint's
	// legacy exception, even while other endpoints keep cutover in project.
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, grantID); err != nil {
		t.Fatal(err)
	}
	switched, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(switched.Entries) != 1 || len(switched.Routes) != 0 {
		t.Fatalf("applied endpoint retained unverified legacy route: entries=%d routes=%d err=%v", len(switched.Entries), len(switched.Routes), err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, legacySnapshot)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err != nil || len(filtered.Routes) != 0 || len(filtered.Entries) != 1 {
		t.Fatalf("snapshot revived legacy route after first apply: entries=%d routes=%d err=%v", len(filtered.Entries), len(filtered.Routes), err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET health_expires_unix_ms=? WHERE id=?`, now.Add(5*time.Second).UnixMilli(), grantID); err != nil {
		t.Fatal(err)
	}
	verified, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(verified.Routes) != 1 {
		t.Fatalf("verified post-switch route was not published: routes=%d err=%v", len(verified.Routes), err)
	}
}
