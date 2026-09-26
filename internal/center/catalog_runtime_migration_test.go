package center

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
)

// A historical fixture is signed as schema 3, never silently converted to a
// schema 4 executable recipe while preparing a migration test.
func signedLegacyCatalogAuditFixture(t *testing.T, private ed25519.PrivateKey, value catalog.Catalog) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["schemaVersion"] = 3
	for _, item := range legacy["apps"].([]any) {
		app := item.(map[string]any)
		delete(app, "packageRevision")
		delete(app, "runtime")
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	envelope := catalog.Envelope{SchemaVersion: 3, KeyID: "legacy-fixture", Payload: base64.RawURLEncoding.EncodeToString(payload), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, payload))}
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCatalogV4MigrationPreservesLegacyHashesTrustAndResources(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 99)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope := signedLegacyCatalogAuditFixture(t, private, catalogLifecycleManifest("1.0.0", "Archived v3"))
	evidence, err := catalog.AuditLegacyEnvelope(envelope, public)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO catalog_sources(id,display_name,url,public_key,enabled,refresh_seconds,generation,revision,created_at,last_checked_at) VALUES('legacy-private','Legacy','https://example.invalid/catalog',?,1,3600,'legacy-generation',9,?,?)`, public, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO catalog_cache(source_id,envelope,fetched_at)VALUES('legacy-private',?,?)`, envelope, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO catalog_manifest_history(source_id,app_id,version,manifest_sha256,first_seen_at)VALUES('legacy-private','catalog-app','1.0.0',?,?)`, evidence[0].SHA256, stamp); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"root":"cm9vdC1hZnRlci1yb3RhdGlvbg==","timestamp":"dGltZXN0YW1w"}`)
	if _, err := legacy.db.Exec(`INSERT INTO official_catalog_trust(channel,revision,target_sha256,observed_at,expires_at,metadata_json,target)VALUES('stable',19,?,?,?,?,'original-target')`, strings.Repeat("a", 64), stamp, stamp, metadata); err != nil {
		t.Fatal(err)
	}
	var beforeApplications string
	if err := legacy.db.QueryRow(`SELECT json_group_array(json_object('id',id,'node',node_id,'key',app_key,'runtime',runtime,'status',status,'generation',runtime_generation)) FROM applications`).Scan(&beforeApplications); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var revision int
	var digest, seenAt string
	if err := migrated.db.QueryRow(`SELECT package_revision,manifest_sha256,first_seen_at FROM catalog_manifest_history WHERE source_id='legacy-private'`).Scan(&revision, &digest, &seenAt); err != nil {
		t.Fatal(err)
	}
	if revision != 0 || digest != evidence[0].SHA256 || seenAt != stamp {
		t.Fatal("legacy immutable identity changed")
	}
	var archived []byte
	if err := migrated.db.QueryRow(`SELECT payload FROM catalog_legacy_evidence WHERE source_id='legacy-private'`).Scan(&archived); err != nil || !bytes.Equal(archived, envelope) {
		t.Fatalf("legacy evidence changed: %v", err)
	}
	var cacheCount int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM catalog_cache`).Scan(&cacheCount); err != nil || cacheCount != 0 {
		t.Fatal("legacy cache still executable", err)
	}
	var accepted uint64
	var retainedMetadata, retainedTarget []byte
	if err := migrated.db.QueryRow(`SELECT revision,metadata_json,target FROM official_catalog_trust WHERE channel='stable'`).Scan(&accepted, &retainedMetadata, &retainedTarget); err != nil {
		t.Fatal(err)
	}
	if accepted != 19 || !bytes.Equal(metadata, retainedMetadata) || string(retainedTarget) != "original-target" {
		t.Fatal("root/revision trust was reset")
	}
	var afterApplications string
	if err := migrated.db.QueryRow(`SELECT json_group_array(json_object('id',id,'node',node_id,'key',app_key,'runtime',runtime,'status',status,'generation',runtime_generation)) FROM applications`).Scan(&afterApplications); err != nil {
		t.Fatal(err)
	}
	if beforeApplications != afterApplications {
		t.Fatal("migration changed installed application identity or runtime state")
	}
	var state, resources string
	if err := migrated.db.QueryRow(`SELECT adoption_state,resources_json FROM application_resources WHERE application_id='application-v3'`).Scan(&state, &resources); err != nil || state != "pending" || resources != "{}" {
		t.Fatal("migration incorrectly claimed observed adoption", err)
	}
	fresh, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !reflect.DeepEqual(databaseSchemaShape(t, fresh.db), databaseSchemaShape(t, migrated.db)) {
		t.Fatal("schema99 migration differs from fresh schema100")
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v99-before-v100-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatal("migration did not preserve pre-upgrade backup", err)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("migration backup permissions", err)
	}
	if version, err := sqliteSchemaVersion(ctx, migrated.db); err != nil || version != 100 {
		t.Fatal("schema version", version, err)
	}
}

func TestCatalogV4MigrationRejectsInflightOrUncertainOperations(t *testing.T) {
	for _, state := range []string{"pending", "running", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyMigrationStore(t, directory, 99)
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			actual := state
			uncertain := 0
			if state == "uncertain" {
				actual = "failed"
				uncertain = 1
			}
			if _, err := legacy.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,reconciliation_required,created_at,updated_at) VALUES('unfinished','application-v3','agent-v3','agent-v3','pulse.enrollment.create','{}',?,?,?,?)`, actual, uncertain, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(directory); err == nil {
				opened.Close()
				t.Fatal("migration accepted unfinished operation")
			}
			db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if version, err := sqliteSchemaVersion(context.Background(), db); err != nil || version != 99 {
				t.Fatal("failed migration partially advanced schema", version, err)
			}
			var storedState string
			var storedUncertain int
			if err := db.QueryRow(`SELECT state,reconciliation_required FROM application_commands WHERE id='unfinished'`).Scan(&storedState, &storedUncertain); err != nil || storedState != actual || storedUncertain != uncertain {
				t.Fatal("failed migration rewrote execution evidence", err)
			}
		})
	}
}
