package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/glivela"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func compositeCenterFixture(t *testing.T) (*Store, string, string, string) {
	t.Helper()
	store, sixID, grantID, _ := openMeridianRuntimeIdentityFixture(t)
	if _, err := store.db.Exec(`UPDATE applications SET image='example.test/xray:reviewed' WHERE id='snapshot-shared-app'`); err != nil {
		t.Fatal(err)
	}
	desired, applied, _ := readMeridianAuthorizationServer(t, store, sixID)
	desired.Plan.EgressIP = "2606:4700:4700::1111"
	applied.Plan.EgressIP = desired.Plan.EgressIP
	writeMeridianAuthorizationServer(t, store, desired, applied, "ready")
	four := enrollOrchestrationNode(t, store, "composite-v4", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63", EnabledKinds: []string{networking.KindHeadscale}})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, four.ID); err != nil {
		t.Fatal(err)
	}
	state, peer := meridianAppliedLandingFixtureJSON(t, four.ID, "100.64.0.63", "100.64.0.61")
	if _, err := store.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,updated_at) VALUES(?,1,1,?,?,?,'ready',?)`, four.ID, state, state, peer, stamp); err != nil {
		t.Fatal(err)
	}
	return store, sixID, four.ID, grantID
}

func TestCompositeHealthRequiresBothFamiliesAndUsesShortestLease(t *testing.T) {
	store, six, four, _ := compositeCenterFixture(t)
	combo, err := store.CreateMeridianRouteGrant(context.Background(), MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	projection, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	now := store.now().UTC().Truncate(time.Millisecond)
	result := meridianHealthResult(projection, now, true)
	for i := range result.Peers {
		if result.Peers[i].EgressID == six {
			result.Peers[i].Status.ExitIP = "2606:4700:4700::1111"
		}
		if result.Peers[i].EgressID == four {
			result.Peers[i].Status.AllowedUntil = now.Add(5 * time.Second)
		}
	}
	if err := recordMeridianRouteHealth(ctx, tx, sharedSnapshotEndpointID, projection, result, now); err != nil {
		t.Fatal(err)
	}
	var healthy int
	var expiry int64
	if err := tx.QueryRow(`SELECT runtime_healthy,health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, combo.ID).Scan(&healthy, &expiry); err != nil {
		t.Fatal(err)
	}
	if healthy != 1 || expiry != now.Add(5*time.Second).UnixMilli() {
		t.Fatal("composite published without two families or beyond its shorter lease")
	}
	for i := range result.Peers {
		if result.Peers[i].EgressID == four {
			result.Peers[i].Status.ExitIP = "2606:4700:4700::1001"
		}
	}
	if err := recordMeridianRouteHealth(ctx, tx, sharedSnapshotEndpointID, projection, result, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT runtime_healthy FROM meridian_route_grants WHERE id=?`, combo.ID).Scan(&healthy); err != nil || healthy != 0 {
		t.Fatal("wrong-family secondary receipt accepted")
	}
}

func TestCompositeNativePolicyRetainsAuthorizedExtensionImage(t *testing.T) {
	store, six, four, _ := compositeCenterFixture(t)
	ctx := context.Background()
	if _, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO node_egress_policies(node_id,policy,revision,verified_revision,updated_at) SELECT node_id,'ipv4_only',1,1,? FROM applications WHERE id='snapshot-shared-app'`, store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	projection, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if projection.task.ImageReference != "example.test/xray:reviewed" || projection.task.NativeEgress != meridian.EgressIPv4Only || len(projection.task.Desired.Files) != 1 {
		t.Fatal("native policy replaced the installed composite runtime")
	}
	if err := projection.task.Validate(); err != nil {
		t.Fatal(err)
	}
	for name := range projection.task.Desired.Files {
		projection.task.Desired.Files[name] = []byte("unbound replacement")
	}
	if projection.task.Validate() == nil {
		t.Fatal("non-stock runtime accepted an unbound composite artifact")
	}
}

func TestCompositePublicationAndSnapshotLoseOnlyUnavailableCombination(t *testing.T) {
	store, six, four, single := compositeCenterFixture(t)
	ctx := context.Background()
	combo, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready',health_expires_unix_ms=? WHERE id IN (?,?)`, store.now().Add(time.Minute).UnixMilli(), single, combo.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	projection, err := store.meridianSubscriptionProjectionInTx(ctx, tx, sharedSnapshotAccountA, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Routes) != 2 || len(projection.Entries) == 0 {
		t.Fatal("both existing and composite subscriptions required")
	}
	if _, err := meridian.RenderMihomo(projection.Entries, sharedSnapshotAccountA, meridian.FixedMode, projection.Routes); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE agents SET last_seen_at='' WHERE id=?`, four); err != nil {
		t.Fatal(err)
	}
	filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, projection)
	if err != nil || len(filtered.Routes) != 1 || filtered.Routes[0].Grant.ID != single || len(filtered.Entries) != len(projection.Entries) {
		t.Fatal("cached subscription preserved offline combination or removed unaffected native/single routes")
	}
}

func TestCompositeCreationCoexistsWithSingleExitWithoutDuplicatingAccounts(t *testing.T) {
	store, six, four, single := compositeCenterFixture(t)
	ctx := context.Background()
	combo, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four})
	if err != nil {
		t.Fatal(err)
	}
	if combo.ID == single || combo.IPv4EgressNodeID != four {
		t.Fatal("single and composite identities collided")
	}
	var accounts, credentials int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_credentials WHERE kind='route' AND account_id=?`, sharedSnapshotAccountA).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if accounts != 2 || credentials != 2 {
		t.Fatal("a second account or per-family credential was created")
	}
	if _, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four}); err == nil {
		t.Fatal("duplicate composite accepted")
	}
	routes, err := readMeridianRuntimeIdentityRoutes(t, store, nil)
	if err != nil || len(routes.grants) != 2 || len(routes.runtimePeers) != 2 || routes.routeSecondaryPeerIDs[combo.ID] != four {
		t.Fatalf("composite runtime projection: %v", err)
	}
}

func TestCompositeSecondaryAuthorizationAndFamilyFailClosed(t *testing.T) {
	for _, condition := range []string{"source removed", "offline", "wrong family", "desired family changed"} {
		t.Run(condition, func(t *testing.T) {
			store, six, four, single := compositeCenterFixture(t)
			combo, err := store.CreateMeridianRouteGrant(context.Background(), MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: six, IPv4EgressNodeID: four})
			if err != nil {
				t.Fatal(err)
			}
			desired, applied, _ := readMeridianAuthorizationServer(t, store, four)
			switch condition {
			case "source removed":
				desired.Plan.Sources = nil
			case "offline":
				if _, err := store.db.Exec(`UPDATE agents SET last_seen_at='' WHERE id=?`, four); err != nil {
					t.Fatal(err)
				}
			case "wrong family":
				desired.Plan.EgressIP = "2606:4700:4700::1001"
				applied.Plan.EgressIP = desired.Plan.EgressIP
			case "desired family changed":
				desired.Plan.EgressIP = "2606:4700:4700::1001"
			}
			writeMeridianAuthorizationServer(t, store, desired, applied, "ready")
			routes, err := readMeridianRuntimeIdentityRoutes(t, store, nil)
			if err != nil || routes.blockedGrants[combo.ID] == "" || len(routes.grants) != 1 || routes.grants[0].ID != single {
				t.Fatalf("secondary failure leaked to native or single route: %v", err)
			}
		})
	}
}

func TestCompositeExitPairBindsDistinctPrivateIdentities(t *testing.T) {
	store, six, four, _ := compositeCenterFixture(t)
	var raw []byte
	if err := store.db.QueryRow(`SELECT peer_json FROM landing_server_states WHERE node_id=?`, six).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var identity landing.PeerIdentity
	if json.Unmarshal(raw, &identity) != nil {
		t.Fatal("fixture")
	}
	identity.Address = "100.64.0.63"
	raw, _ = json.Marshal(identity)
	if _, err := store.db.Exec(`UPDATE landing_server_states SET peer_json=? WHERE node_id=?`, raw, four); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := store.readCompositeRoutePeers(context.Background(), tx, sharedSnapshotEndpointID, six, four, true); err == nil {
		t.Fatal("same Tailscale machine identity accepted twice")
	}
}
