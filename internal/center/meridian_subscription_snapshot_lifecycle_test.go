package center

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/networking"
)

func TestMeridianPartialLiveSubscriptionDoesNotOverwriteAppliedSnapshot(t *testing.T) {
	for _, pending := range []struct {
		name, query string
	}{
		{"runtime", `UPDATE meridian_endpoints SET desired_revision=2,runtime_healthy=0,status='pending' WHERE id='snapshot-second-endpoint'`},
		{"publication", `UPDATE publications SET desired_revision=2,status='pending' WHERE id='snapshot-second-publication'`},
		{"service", `UPDATE services SET status='pending' WHERE id='snapshot-second-service'`},
	} {
		t.Run(pending.name, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			addMeridianSnapshotSecondEndpoint(t, store)
			ctx := context.Background()
			initial, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(initial.Entries) != 2 {
				t.Fatalf("initial subscription: entries=%d err=%v", len(initial.Entries), err)
			}
			var originalDigest string
			if err := store.db.QueryRowContext(ctx, `SELECT content_sha256 FROM meridian_subscription_snapshots WHERE account_id=?`, sharedSnapshotAccountB).Scan(&originalDigest); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, pending.query); err != nil {
				t.Fatal(err)
			}
			partial, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(partial.Entries) != 1 || partial.Entries[0].Material.Credential.EntryID != "snapshot-shared-app" {
				t.Fatalf("pending entry changed partial serving policy: entries=%#v err=%v", partial.Entries, err)
			}
			var currentDigest string
			if err := store.db.QueryRowContext(ctx, `SELECT content_sha256 FROM meridian_subscription_snapshots WHERE account_id=?`, sharedSnapshotAccountB).Scan(&currentDigest); err != nil {
				t.Fatal(err)
			}
			if currentDigest != originalDigest {
				t.Fatal("partial live output replaced the complete applied snapshot")
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending'`); err != nil {
				t.Fatal(err)
			}
			recovered, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(recovered.Entries) != 2 {
				t.Fatalf("complete applied snapshot was not recoverable: entries=%d err=%v", len(recovered.Entries), err)
			}
		})
	}
}

func TestMeridianAppliedSnapshotHonorsExplicitNativeRemoval(t *testing.T) {
	for _, removal := range []struct {
		name, query string
	}{
		{"disabled-credential", `UPDATE meridian_credentials SET enabled=0 WHERE account_id='snapshot-account-b'`},
		{"removed-credential", `DELETE FROM meridian_credentials WHERE account_id='snapshot-account-b'`},
		{"retired-endpoint", `UPDATE meridian_endpoints SET status='retired'`},
		{"stopped-service", `UPDATE services SET status='stopped' WHERE id='snapshot-shared-service'`},
		{"stopped-publication", `UPDATE publications SET status='stopped' WHERE id='snapshot-shared-publication'`},
		{"removed-publication", `DELETE FROM publications WHERE id='snapshot-shared-publication'`},
		{"disabled-vless", `UPDATE meridian_endpoints SET vless_enabled=0,hy2_enabled=1,hy2_inbound_tag='snapshot-hy2',hy2_server_name='entry.example.test',hy2_certificate_secret_id=private_key_secret_id,hy2_private_key_secret_id=private_key_secret_id`},
	} {
		t.Run(removal.name, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			ctx := context.Background()
			initial, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(initial.Entries) != 1 {
				t.Fatalf("initial subscription: entries=%d err=%v", len(initial.Entries), err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=2,runtime_healthy=0,status='pending'`); err != nil {
				t.Fatal(err)
			}
			if pending, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil || len(pending.Entries) != 1 {
				t.Fatalf("pending snapshot: entries=%d err=%v", len(pending.Entries), err)
			}
			if _, err := store.db.ExecContext(ctx, removal.query); err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			snapshot, err := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
			if err != nil {
				t.Fatal(err)
			}
			filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
			if err != nil || len(filtered.Entries) != 0 || len(filtered.Routes) != 0 {
				t.Fatalf("explicitly removed native remained in snapshot: entries=%d routes=%d err=%v", len(filtered.Entries), len(filtered.Routes), err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); !errors.Is(err, errMeridianSubscriptionNotFound) {
				t.Fatalf("explicitly removed native was published: %v", err)
			}
		})
	}
}

func TestMeridianAppliedSnapshotRemovesRouteWhenNativeAuthorityIsRevoked(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	egress := enrollOrchestrationNode(t, store, "snapshot-route-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
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
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready' WHERE id=?`, grant.ID); err != nil {
		t.Fatal(err)
	}
	initial, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(initial.Entries) != 1 || len(initial.Routes) != 1 {
		t.Fatalf("initial fixed route: entries=%d routes=%d err=%v", len(initial.Entries), len(initial.Routes), err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_credentials SET enabled=0 WHERE account_id=? AND kind='native'`, sharedSnapshotAccountB); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
	if err != nil || len(filtered.Entries) != 0 || len(filtered.Routes) != 0 {
		t.Fatalf("fixed route survived revoked native authority: entries=%d routes=%d err=%v", len(filtered.Entries), len(filtered.Routes), err)
	}
}

func addMeridianSnapshotSecondEndpoint(t *testing.T, store *Store) {
	t.Helper()
	node := enrollOrchestrationNode(t, store, "snapshot-second-entry", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.62", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.62", HeadscaleAddress: "100.64.0.62", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES('snapshot-second-app','Second entry',?,?,?,'','running','docker','',?,?)`, node.ID, siteID, meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES('snapshot-second-service','snapshot-second-app',?,'second-entry','Second entry','tcp',443,443,'100.64.0.62:443','observed',?,0,'0.0.0.0','ready',?,?)`, siteID, meridianEntryProtocol, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("second-private-key"), meridianEndpointSecretContext("snapshot-second-endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES('snapshot-second-endpoint','snapshot-second-app','snapshot-second-service','second-entry',443,'second.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'second-public-key','["abcd"]','chrome',1,1,1,'ready',?,?)`, secretID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES('snapshot-second-publication','snapshot-second-service','public_shared_443','application_node',?,'second.example.test','www.example.com','manual',0,1,1,'ready',?,?)`, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.insertMeridianCredential(ctx, tx, sharedSnapshotAccountB, "snapshot-second-endpoint", "snapshot-second-app", meridian.NativeCredential, "", true, stamp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
