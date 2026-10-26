package center

import (
	"context"
	"database/sql"
	"github.com/petauron/glivela"
	"testing"
	"time"
)

func TestFreshMeridianSchemaRejectsNegativeRouteHealthExpiry(t *testing.T) {
	store, _, grantID, _ := openMeridianRuntimeIdentityFixture(t)
	var expires int64
	if err := store.db.QueryRow(`SELECT health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, grantID).Scan(&expires); err != nil || expires != 0 {
		t.Fatalf("new route health expiry=%d err=%v", expires, err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_route_grants SET health_expires_unix_ms=-1 WHERE id=?`, grantID); err == nil {
		t.Fatal("fresh schema accepted a negative health expiry")
	}
}

func seedMeridianVersion88HealthFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at)
		VALUES('agent-v3',1,'{"id":"current-observation","publicKey":"test-source-key","address":"100.64.0.71"}',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('v88-health-endpoint-material',X'010203',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES('v88-health-endpoint','application-v3','service-v3','v88-entry',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]','v88-health-endpoint-material','test-public-key','["abcd"]',5,5,1,'ready',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"ready", "pending", "revoking", "revoked", "failed"} {
		accountID := "v88-health-account-" + state
		for _, suffix := range []string{"subscription", "native", "route", "snapshot"} {
			// Opaque synthetic envelopes exercise byte-for-byte preservation;
			// this migration never decrypts or regenerates account material.
			if _, err := db.ExecContext(ctx, `INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES(?,X'040506',?,?)`, accountID+"-"+suffix, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		enabled, healthy := 1, 1
		if state == "revoking" || state == "revoked" {
			enabled = 0
		}
		if state == "failed" {
			healthy = 0
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,reset_days,next_reset_at,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
			VALUES(?,?,1048576,30,'2099-01-01T00:00:00Z',1,?,?,7,7,'active',?,?)`, accountID, accountID, accountID+"-subscription", glivela.SubscriptionTokenFingerprint(accountID), stamp, stamp); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"native", "route"} {
			credentialID := accountID + "-" + kind
			var egress any
			if kind == "route" {
				egress = "agent-v3"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at)
				VALUES(?,?,'v88-health-endpoint',?,?,?,?,?,1,?,?)`, credentialID, accountID, kind, credentialID, glivela.Identity(credentialID), credentialID, egress, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) VALUES(?,100,450,200,250,?)`, credentialID, stamp); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,hide_native,enabled,desired_revision,applied_revision,runtime_healthy,status,last_error,created_at,updated_at)
			VALUES(?,?,'v88-health-endpoint','agent-v3',?,?,1,?,3,3,?,?,?, ?,?)`, "v88-health-grant-"+state, accountID, accountID+"-native", accountID+"-route", enabled, healthy, state, "previous "+state+" diagnostic", stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO meridian_subscription_snapshots(account_id,secret_id,content_sha256,account_revision,created_at,updated_at) VALUES(?,?,?,7,?,?)`, accountID, accountID+"-snapshot", glivela.Identity(accountID+"-snapshot"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

// Private listen_port is intentionally migrated in v102 and tested there.
// Public subscription addresses, credentials and usage remain invariant.
