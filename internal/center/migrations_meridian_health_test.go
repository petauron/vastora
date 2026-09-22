package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/petauron/meridian"
)

func TestVersion89RequiresFreshLandingHealthWithoutChangingIdentityOrQuota(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 88)
	seedMeridianVersion88HealthFixture(t, legacy.db)
	before := meridianVersion88PreservedState(t, legacy.db)
	if err := legacy.db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if after := meridianVersion88PreservedState(t, migrated.db); !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed Meridian identities, configuration, revisions, subscription material, or quota usage")
	}
	var sourceJSON string
	if err := migrated.db.QueryRow(`SELECT source_peer_json FROM meridian_endpoints WHERE id='v88-health-endpoint'`).Scan(&sourceJSON); err != nil || sourceJSON != "{}" {
		t.Fatalf("migration authorized a mutable current source: source present=%t err=%v", sourceJSON != "{}", err)
	}
	for _, expected := range []struct {
		id, status, message string
	}{
		{id: "ready", status: "blocked", message: "Awaiting fresh direct entry-to-landing runtime evidence."},
		{id: "pending", status: "pending", message: "previous pending diagnostic"},
		{id: "revoking", status: "revoking", message: "previous revoking diagnostic"},
		{id: "revoked", status: "revoked", message: "previous revoked diagnostic"},
		{id: "failed", status: "failed", message: "previous failed diagnostic"},
	} {
		var status, message string
		var healthy int
		var expires int64
		if err := migrated.db.QueryRow(`SELECT status,runtime_healthy,health_expires_unix_ms,last_error FROM meridian_route_grants WHERE id=?`, "v88-health-grant-"+expected.id).Scan(&status, &healthy, &expires, &message); err != nil {
			t.Fatal(err)
		}
		if status != expected.status || healthy != 0 || expires != 0 || message != expected.message {
			t.Fatalf("route %s health was incorrectly inherited: status=%s healthy=%d expires=%d message=%q", expected.id, status, healthy, expires, message)
		}
	}
	if _, err := migrated.db.Exec(`UPDATE meridian_route_grants SET health_expires_unix_ms=-1 WHERE id='v88-health-grant-ready'`); err == nil {
		t.Fatal("migrated schema accepted a negative health expiry")
	}
	var version, violations int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("migrated version=%d err=%v", version, err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("migration broke foreign keys: count=%d err=%v", violations, err)
	}
	pattern := filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v88-before-v%d-*.db", centerSchemaVersion))
	backups, err := filepath.Glob(pattern)
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one pre-migration backup: count=%d err=%v", len(backups), err)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("migration backup permissions are not private: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if saved := meridianVersion88PreservedState(t, backup); !reflect.DeepEqual(saved, before) {
		t.Fatal("backup did not preserve the pre-migration Meridian state")
	}
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 88 {
		t.Fatalf("backup version=%d err=%v", version, err)
	}
	var legacyHealthy, futureColumns int
	if err := backup.QueryRow(`SELECT runtime_healthy FROM meridian_route_grants WHERE id='v88-health-grant-ready'`).Scan(&legacyHealthy); err != nil || legacyHealthy != 1 {
		t.Fatalf("backup was taken after health invalidation: healthy=%d err=%v", legacyHealthy, err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('meridian_route_grants') WHERE name='health_expires_unix_ms'`).Scan(&futureColumns); err != nil || futureColumns != 0 {
		t.Fatalf("backup was not a real version 88 schema: future columns=%d err=%v", futureColumns, err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('meridian_endpoints') WHERE name='source_peer_json'`).Scan(&futureColumns); err != nil || futureColumns != 0 {
		t.Fatalf("backup already contained the new source authority: columns=%d err=%v", futureColumns, err)
	}

	// Reopening must not re-run the one-time health invalidation after a fresh
	// observation has been accepted by the current release.
	expires := time.Now().UTC().Add(time.Minute).UnixMilli()
	if _, err := migrated.db.Exec(`UPDATE meridian_route_grants SET status='ready',runtime_healthy=1,health_expires_unix_ms=?,last_error='' WHERE id='v88-health-grant-ready'`, expires); err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var reopenedHealthy int
	var reopenedExpiry int64
	if err := reopened.db.QueryRow(`SELECT runtime_healthy,health_expires_unix_ms FROM meridian_route_grants WHERE id='v88-health-grant-ready'`).Scan(&reopenedHealthy, &reopenedExpiry); err != nil || reopenedHealthy != 1 || reopenedExpiry != expires {
		t.Fatalf("reopening replayed health migration: healthy=%d expiry=%d err=%v", reopenedHealthy, reopenedExpiry, err)
	}
	if repeated, err := filepath.Glob(pattern); err != nil || len(repeated) != 1 {
		t.Fatalf("reopening created another migration backup: count=%d err=%v", len(repeated), err)
	}
}

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
	if _, err := db.ExecContext(ctx, `INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at)
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
			VALUES(?,?,1048576,30,'2099-01-01T00:00:00Z',1,?,?,7,7,'active',?,?)`, accountID, accountID, accountID+"-subscription", meridian.SubscriptionTokenFingerprint(accountID), stamp, stamp); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"native", "route"} {
			credentialID := accountID + "-" + kind
			var egress any
			if kind == "route" {
				egress = "agent-v3"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at)
				VALUES(?,?,'v88-health-endpoint',?,?,?,?,?,1,?,?)`, credentialID, accountID, kind, credentialID, meridian.Identity(credentialID), credentialID, egress, stamp, stamp); err != nil {
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
		if _, err := db.ExecContext(ctx, `INSERT INTO meridian_subscription_snapshots(account_id,secret_id,content_sha256,account_revision,created_at,updated_at) VALUES(?,?,?,7,?,?)`, accountID, accountID+"-snapshot", meridian.Identity(accountID+"-snapshot"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

func meridianVersion88PreservedState(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	queries := map[string]string{
		"endpoints": `SELECT id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,
			private_key_secret_id,public_key,short_ids_json,fingerprint,vless_enabled,hy2_enabled,hy2_inbound_tag,hy2_server_name,
			hy2_certificate_secret_id,hy2_private_key_secret_id,hy2_certificate_not_after,desired_revision,applied_revision,runtime_healthy,legacy_retired,status,last_error,created_at,updated_at
			FROM meridian_endpoints ORDER BY id`,
		"accounts":    `SELECT * FROM meridian_accounts ORDER BY id`,
		"credentials": `SELECT * FROM meridian_credentials ORDER BY id`,
		"usage":       `SELECT * FROM meridian_usage_watermarks ORDER BY credential_id`,
		"snapshots":   `SELECT * FROM meridian_subscription_snapshots ORDER BY account_id`,
		"secrets":     `SELECT * FROM secrets WHERE id GLOB 'v88-health-*' ORDER BY id`,
		"grants":      `SELECT id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,mode,hide_native,enabled,desired_revision,applied_revision,created_at,updated_at FROM meridian_route_grants ORDER BY id`,
	}
	result := make(map[string]string, len(queries))
	for name, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		digest := sha256.New()
		encoder := json.NewEncoder(digest)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if err := encoder.Encode(values); err != nil {
				rows.Close()
				t.Fatal(err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		result[name] = fmt.Sprintf("%x", digest.Sum(nil))
	}
	return result
}
