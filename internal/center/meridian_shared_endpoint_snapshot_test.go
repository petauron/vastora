package center

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

const (
	sharedSnapshotEndpointID = "snapshot-shared-endpoint"
	sharedSnapshotAccountA   = "snapshot-account-a"
	sharedSnapshotAccountB   = "snapshot-account-b"
	sharedSnapshotTokenB     = "snapshot-subscription-b"
	sharedSnapshotUUIDB      = "22222222-2222-4222-8222-222222222222"
)

func TestMeridianAccountMetadataAndResetKeepRenderedRuntime(t *testing.T) {
	for _, operation := range []string{"account-update", "scheduled-reset"} {
		t.Run(operation, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			ctx := context.Background()
			if _, err := store.db.ExecContext(ctx, `UPDATE applications SET image='example.test/xray:current' WHERE id='snapshot-shared-app'`); err != nil {
				t.Fatal(err)
			}
			project := func() meridianRuntimeProjection {
				t.Helper()
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				projection, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
				if err != nil {
					t.Fatal(err)
				}
				return projection
			}
			if operation == "scheduled-reset" {
				future := store.now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
				if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET reset_days=1,next_reset_at=? WHERE id=?`, future, sharedSnapshotAccountA); err != nil {
					t.Fatal(err)
				}
			}
			before := project().task
			if operation == "account-update" {
				enabled := true
				if _, err := store.UpdateMeridianAccount(ctx, sharedSnapshotAccountA, MeridianAccountInput{DisplayName: sharedSnapshotAccountA, TotalBytes: 1000, Enabled: &enabled}); err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				past := store.now().UTC().Add(-time.Minute)
				if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET reset_days=1,next_reset_at=? WHERE id=?`, past.Format(time.RFC3339Nano), sharedSnapshotAccountA); err != nil {
					t.Fatal(err)
				}
				if err := store.resetDueMeridianAccounts(ctx, tx); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			after := project().task
			if after.Desired.Revision != before.Desired.Revision+1 || after.Desired.ConfigSHA256 != before.Desired.ConfigSHA256 ||
				!reflect.DeepEqual(after.Desired.Config, before.Desired.Config) || !reflect.DeepEqual(after.Peers, before.Peers) || !reflect.DeepEqual(after.Source, before.Source) {
				t.Fatalf("%s unexpectedly changed the running Xray authority: before=%d/%s after=%d/%s", operation, before.Desired.Revision, before.Desired.ConfigSHA256, after.Desired.Revision, after.Desired.ConfigSHA256)
			}
		})
	}
}

func TestMeridianObservationKeepsImportedServiceIdentityAndRepairsListener(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET name='inbound-8',app_protocol='vless/tcp/reality' WHERE id='snapshot-shared-service'`); err != nil {
		t.Fatal(err)
	}
	observation := []ApplicationEndpointObservation{{AppKey: meridianAppKey, Name: "inbound-1", Protocol: "tcp", AppProtocol: meridianEntryProtocol, Listen: "0.0.0.0", Port: 443, Enabled: true, InboundTag: "shared-entry"}}
	var nodeID string
	if err := store.db.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	reconcile := func() {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var cleanups []publicationCleanup
		if err := store.reconcileMeridianEndpoints(ctx, tx, nodeID, observation, store.now().UTC(), &cleanups); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// The imported inbound number is not the Agent's normalized observation
	// name. Its stable tag, service ID and subscription identity must survive.
	reconcile()
	var name, protocol string
	var services int
	if err := store.db.QueryRowContext(ctx, `SELECT name,app_protocol FROM services WHERE id='snapshot-shared-service'`).Scan(&name, &protocol); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE application_id='snapshot-shared-app'`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if name != "inbound-8" || protocol != meridianEntryProtocol || services != 1 {
		t.Fatalf("Meridian observation changed imported identity: name=%q protocol=%q services=%d", name, protocol, services)
	}
	var revision int64
	var encoded []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,desired_json FROM node_listener_states WHERE node_id=?`, nodeID).Scan(&revision, &encoded); err != nil {
		t.Fatal(err)
	}
	var listener gateway.NodeListenerState
	if err := json.Unmarshal(encoded, &listener); err != nil {
		t.Fatal(err)
	}
	if len(listener.Listener.Routes) != 1 || listener.Listener.Routes[0].ProxyProtocol != gateway.ProxyProtocolV2 {
		t.Fatalf("repaired Meridian listener lacks PROXY v2: %#v", listener.Listener.Routes)
	}
	reconcile()
	var nextRevision int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM node_listener_states WHERE node_id=?`, nodeID).Scan(&nextRevision); err != nil {
		t.Fatal(err)
	}
	if nextRevision != revision {
		t.Fatalf("unchanged Meridian observation churned listener revision: %d to %d", revision, nextRevision)
	}
}

func TestMeridianSharedEndpointMutationsPreserveOtherAccountSubscription(t *testing.T) {
	for _, operation := range []string{"account-update", "scheduled-reset", "account-expiry"} {
		t.Run(operation, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			ctx := context.Background()
			var initialSnapshots int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_subscription_snapshots`).Scan(&initialSnapshots); err != nil || initialSnapshots != 0 {
				t.Fatalf("fixture already downloaded subscriptions: snapshots=%d err=%v", initialSnapshots, err)
			}

			if operation == "account-update" {
				enabled := true
				if _, err := store.UpdateMeridianAccount(ctx, sharedSnapshotAccountA, MeridianAccountInput{DisplayName: "Updated account A", TotalBytes: 2000, Enabled: &enabled}); err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				past := store.now().UTC().Add(-time.Minute)
				if operation == "scheduled-reset" {
					_, err = tx.ExecContext(ctx, `UPDATE meridian_accounts SET reset_days=1,next_reset_at=? WHERE id=?`, past.Format(time.RFC3339Nano), sharedSnapshotAccountA)
				} else {
					_, err = tx.ExecContext(ctx, `UPDATE meridian_accounts SET expiry_time=? WHERE id=?`, past.UnixMilli(), sharedSnapshotAccountA)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := store.resetDueMeridianAccounts(ctx, tx); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}

			var endpointDesired, endpointApplied, healthy int
			var endpointStatus string
			if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,runtime_healthy,status FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&endpointDesired, &endpointApplied, &healthy, &endpointStatus); err != nil {
				t.Fatal(err)
			}
			if endpointDesired != 2 || endpointApplied != 1 || healthy != 0 || endpointStatus != "pending" {
				t.Fatalf("mutation did not invalidate the shared runtime: desired=%d applied=%d healthy=%d status=%s", endpointDesired, endpointApplied, healthy, endpointStatus)
			}
			var accountDesired, accountApplied, accountEnabled int
			var accountStatus string
			if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,enabled,status FROM meridian_accounts WHERE id=?`, sharedSnapshotAccountB).Scan(&accountDesired, &accountApplied, &accountEnabled, &accountStatus); err != nil {
				t.Fatal(err)
			}
			if accountDesired != 1 || accountApplied != 1 || accountEnabled != 1 || accountStatus != "active" {
				t.Fatalf("unrelated account was changed: desired=%d applied=%d enabled=%d status=%s", accountDesired, accountApplied, accountEnabled, accountStatus)
			}

			// Inspect before the first download: rendering the subscription must
			// not be what first creates the recovery evidence under test.
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, snapshotErr := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if snapshotErr != nil || snapshot.AccountRevision != 1 || len(snapshot.Entries) != 1 || snapshot.Entries[0].Material.Credential.AccountID != sharedSnapshotAccountB {
				t.Fatalf("unrelated account lost its applied snapshot: revision=%d entries=%d err=%v", snapshot.AccountRevision, len(snapshot.Entries), snapshotErr)
			}
			server := NewServer(store, t.TempDir(), false)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/"+sharedSnapshotTokenB, nil))
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(response.Body.String()))
			if response.Code != http.StatusOK || err != nil || !strings.Contains(string(decoded), "vless://"+sharedSnapshotUUIDB+"@entry.example.test:443") {
				t.Fatalf("unrelated subscription failed during rebuild: status=%d body=%q err=%v", response.Code, decoded, err)
			}
			if got := response.Header().Get("Subscription-Userinfo"); got != "upload=0; download=50; total=1000" {
				t.Fatalf("unrelated account quota changed: %q", got)
			}
		})
	}
}

func openMeridianSharedEndpointSnapshotFixture(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := enrollOrchestrationNode(t, store, "shared-subscription-entry", NodeCapabilities{Docker: true},
		[]networking.Candidate{
			{Address: "100.64.0.61", Interface: "tailscale0", Kind: networking.KindHeadscale},
			{Address: "203.0.113.61", Interface: "eth0", Kind: networking.KindPublic},
		},
		networking.Profile{ServiceAddress: "100.64.0.61", HeadscaleAddress: "100.64.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindHeadscale, networking.KindPublic}, DirectPublic: true})
	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, now, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at)
		VALUES(?,?,?,?)`, node.ID, landing.ClientRuntimeGeneration, []byte(`{"id":"tailnet-source-entry","publicKey":"nodekey:test-source","address":"100.64.0.61"}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES('snapshot-shared-app','Shared entry',?,?,?,'','running','docker','',?,?)`, node.ID, siteID, meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES('snapshot-shared-service','snapshot-shared-app',?,'shared-entry','Shared entry','tcp',443,443,'100.64.0.61:443','observed',?,0,'0.0.0.0','ready',?,?)`, siteID, meridianEntryProtocol, now, now); err != nil {
		t.Fatal(err)
	}
	endpointSecretID, err := store.putSecret(ctx, tx, []byte("test-private-key"), meridianEndpointSecretContext(sharedSnapshotEndpointID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES(?,'snapshot-shared-app','snapshot-shared-service','shared-entry',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'test-public-key','["abcd"]','chrome',1,1,1,'ready',?,?)`, sharedSnapshotEndpointID, endpointSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES('snapshot-shared-publication','snapshot-shared-service','public_shared_443','application_node',?,'entry.example.test','www.example.com','manual',0,1,1,'ready',?,?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct {
		id, token, protocolID string
		usage                 int
	}{
		{sharedSnapshotAccountA, "snapshot-subscription-a", "11111111-1111-4111-8111-111111111111", 100},
		{sharedSnapshotAccountB, sharedSnapshotTokenB, sharedSnapshotUUIDB, 50},
	} {
		tokenSecretID, err := store.putSecret(ctx, tx, []byte(account.token), meridianAccountSecretContext(account.id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
			VALUES(?,?,1000,1,?,?,1,1,'active',?,?)`, account.id, account.id, tokenSecretID, meridian.SubscriptionTokenFingerprint(account.token), now, now); err != nil {
			t.Fatal(err)
		}
		credentialID := account.id + "-native"
		credentialSecretID, err := store.putSecret(ctx, tx, []byte(account.protocolID), meridianCredentialSecretContext(credentialID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at)
			VALUES(?,?,?,'native',?,?,?,1,?,?)`, credentialID, account.id, sharedSnapshotEndpointID, credentialID, meridian.Identity(account.protocolID), credentialSecretID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,observed_bytes,observed_at) VALUES(?,?,?)`, credentialID, account.usage, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store
}
