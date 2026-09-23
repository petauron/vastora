package center

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

func TestOpenMigratesVersion3WithoutLosingPublicationsOrRoutes(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)

	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	version, err := sqliteSchemaVersion(ctx, store.db)
	if err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version = %d, err = %v", version, err)
	}
	provider, err := newMigrationProvider(store.db)
	if err != nil {
		t.Fatal(err)
	}
	migrationVersion, err := provider.GetDBVersion(ctx)
	if err != nil || migrationVersion != centerSchemaVersion {
		t.Fatalf("migration version = %d, err = %v", migrationVersion, err)
	}
	for table, expected := range map[string]int{"publications": 1, "routes": 1} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != expected {
			t.Fatalf("%s count = %d, err = %v", table, count, err)
		}
	}
	var operatingSystem, architecture string
	if err := store.db.QueryRowContext(ctx, `SELECT operating_system, architecture FROM agents WHERE id = 'agent-v3'`).Scan(&operatingSystem, &architecture); err != nil || operatingSystem != "linux" || architecture != "amd64" {
		t.Fatalf("migrated Agent platform = %s/%s, err = %v", operatingSystem, architecture, err)
	}
	var routePublication string
	if err := store.db.QueryRowContext(ctx, `SELECT publication_id FROM routes WHERE id = 'route-v3'`).Scan(&routePublication); err != nil || routePublication != "publication-v3" {
		t.Fatalf("route publication = %q, err = %v", routePublication, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(
		id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, status, created_at, updated_at
	) VALUES('publication-v4', 'service-v3', 'public_shared_443', 'application_node', 'agent-v3', 'raw.example.test', 'manual', 'pending', ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("new publication kind was not accepted: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v3-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backups = %v, err = %v", backups, err)
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("migration backup mode = %v", info.Mode().Perm())
	}
	backupDB, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	backupVersion, err := sqliteSchemaVersion(ctx, backupDB)
	if err != nil || backupVersion != schemaBaselineVersion {
		t.Fatalf("backup schema version = %d, err = %v", backupVersion, err)
	}
}

func TestVersion8MigrationKeepsShared443SNISeparateFromConnectionHostname(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at) VALUES('shared-v7', 'service-v3', 'public_shared_443', 'agent-v3', 'reality.legacy.example.test', 'manual', 'pending', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var connectHostname, sniHostname string
	if err := migrated.db.QueryRowContext(ctx, `SELECT hostname, sni_hostname FROM publications WHERE id = 'shared-v7'`).Scan(&connectHostname, &sniHostname); err != nil {
		t.Fatal(err)
	}
	if connectHostname != "reality.legacy.example.test" || sniHostname != connectHostname {
		t.Fatalf("migrated shared 443 hostname=%q sni=%q", connectHostname, sniHostname)
	}
}

func TestVersion26MigrationQuarantinesEveryExistingRealityPublication(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		 VALUES('reality-v25-app', '3x-ui', 'agent-v3', 'site-v3', 'vastora-official/3x-ui', '', 'running', 'docker', 'master', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at)
		 VALUES('reality-v25-service', 'reality-v25-app', 'site-v3', 'inbound-17', 'Legacy REALITY', 'tcp', 20000, 20000, '10.0.0.17:20000', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, result_json, state, created_at, updated_at)
		 VALUES('reality-v25-command', 'reality-v25-app', 'site-v3', 'Legacy REALITY', 'agent-v3', 'agent-v3', '3xui.reality.create', '{"inboundTag":"vastora-legacy"}', '{"inboundId":17,"target":"www.example.com:443","sniHostname":"www.example.com"}', 'succeeded', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		 VALUES('reality-v25-publication', 'reality-v25-service', 'public_shared_443', 'agent-v3', 'reality.legacy.example.test', 'www.example.com', 'manual', 1, 1, 'ready', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var targetHost, serverName, guardStatus, guardError, serviceStatus, publicationStatus string
	if err := migrated.db.QueryRowContext(ctx, `SELECT guard.target_host, guard.server_name, guard.status, guard.last_error, service.status, publication.status
		FROM three_x_ui_reality_guards guard JOIN services service ON service.id = guard.service_id
		JOIN publications publication ON publication.service_id = service.id
		WHERE guard.service_id = 'reality-v25-service'`).Scan(&targetHost, &serverName, &guardStatus, &guardError, &serviceStatus, &publicationStatus); err != nil {
		t.Fatal(err)
	}
	if targetHost != "www.example.com" || serverName != "www.example.com" || guardStatus != "action_required" || serviceStatus != "degraded" || publicationStatus != "stopped" || !strings.Contains(guardError, "disabled") {
		t.Fatalf("migrated target=%q sni=%q guard=%q error=%q service=%q publication=%q", targetHost, serverName, guardStatus, guardError, serviceStatus, publicationStatus)
	}
	var version int64
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestVersion40MigrationWithdrawsRealityBeforeProxyProtocolCutover(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 39); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		 VALUES('reality-v39-app', '3x-ui', 'agent-v3', 'site-v3', 'vastora-official/3x-ui', '', 'running', 'docker', 'master', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at)
		 VALUES('reality-v39-service', 'reality-v39-app', 'site-v3', 'inbound-9', 'Managed REALITY', 'tcp', 20000, 20000, '10.0.0.9:20000', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, node_asn, target_asn, companion_inbound_id, companion_tag, companion_port, status, verified_at, created_at, updated_at)
		 VALUES('reality-v39-service', 'www.example.com', '203.0.113.9', 'www.example.com', 64500, 64500, 10, 'vastora-guard', 21000, 'ready', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		 VALUES('reality-v39-publication', 'reality-v39-service', 'public_shared_443', 'agent-v3', 'reality.example.test', 'www.example.com', 'manual', 1, 1, 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, status, created_at, updated_at)
		 VALUES('reality-v39-route', 'reality-v39-publication', 'site-v3', 'reality-v39-service', 'agent-v3', 'reality.example.test', 'https', '["10.0.0.9:20000"]', 'ready', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var guardStatus, guardError, serviceStatus, publicationStatus string
	if err := migrated.db.QueryRowContext(ctx, `SELECT guard.status, guard.last_error, service.status, publication.status
		FROM three_x_ui_reality_guards guard JOIN services service ON service.id = guard.service_id
		JOIN publications publication ON publication.service_id = service.id
		WHERE guard.service_id = 'reality-v39-service'`).Scan(&guardStatus, &guardError, &serviceStatus, &publicationStatus); err != nil {
		t.Fatal(err)
	}
	var routes int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE id = 'reality-v39-route'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if guardStatus != "action_required" || !strings.Contains(guardError, "Proxy Protocol v2") || serviceStatus != "degraded" || publicationStatus != "stopped" || routes != 0 {
		t.Fatalf("Proxy Protocol cutover guard=%q error=%q service=%q publication=%q routes=%d", guardStatus, guardError, serviceStatus, publicationStatus, routes)
	}
}

func TestVersion27MigrationBackfillsImmutableCatalogHistory(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := catalogLifecycleManifest("1.0.0", "Migrated manifest")
	setCatalogIntegerDefault(&manifest, `1e0`)
	rawEnvelope := signedCatalogEnvelope(t, privateKey, manifest)
	fetchedAt := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at)
		VALUES(?, ?, ?, ?, 1, 3600, ?)`, "migration-source", "Migration source", "https://catalog.example.invalid", publicKey, fetchedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, etag, last_modified, fetched_at) VALUES(?, ?, ?, ?, ?)`,
		"migration-source", rawEnvelope, `"v1"`, "Sat, 30 Aug 2026 04:00:00 GMT", fetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var generation, checkedAt, historyVersion string
	var revision int64
	if err := store.db.QueryRowContext(ctx, `SELECT generation, revision, last_checked_at FROM catalog_sources WHERE id = ?`, "migration-source").Scan(&generation, &revision, &checkedAt); err != nil {
		t.Fatal(err)
	}
	if generation == "" || revision != 1 || checkedAt != fetchedAt {
		t.Fatalf("migrated source generation=%q revision=%d checkedAt=%q", generation, revision, checkedAt)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM catalog_manifest_history WHERE source_id = ? AND app_id = ?`, "migration-source", "catalog-app").Scan(&historyVersion); err != nil {
		t.Fatal(err)
	}
	if historyVersion != "1.0.0" {
		t.Fatalf("backfilled immutable version = %q", historyVersion)
	}
	changed := catalogLifecycleManifest("1.0.0", "Changed after migration")
	setCatalogIntegerDefault(&changed, `1`)
	if err := commitCatalogForTest(ctx, store, "migration-source", signedCatalogEnvelope(t, privateKey, changed), "", ""); err == nil || !strings.Contains(err.Error(), "immutable catalog manifest changed") {
		t.Fatalf("migrated immutable history was bypassed: %v", err)
	}
	if _, err := catalog.CanonicalAppManifest(manifest.Apps[0]); err != nil {
		t.Fatalf("migration fixture is invalid: %v", err)
	}
}

func TestVersion42MigrationDropsOnlyLegacyCatalogCache(t *testing.T) {
	directory := t.TempDir()
	store := legacyMigrationStore(t, directory, 41)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	publicKey := make([]byte, ed25519.PublicKeySize)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO catalog_sources(
		id, display_name, url, public_key, enabled, refresh_seconds, generation, revision, created_at, last_checked_at, last_error
	) VALUES('legacy-catalog-v2', 'Legacy v2', 'https://catalog.example.invalid/v2', ?, 1, 3600, 'legacy-generation', 1, ?, ?, '')`, publicKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, fetched_at)
		VALUES('legacy-catalog-v2', '{"schemaVersion":2,"keyId":"legacy","payload":"","signature":""}', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO catalog_manifest_history(source_id, app_id, version, manifest_sha256, first_seen_at)
		VALUES('legacy-catalog-v2', 'legacy-app', '1.0.0', ?, ?)`, strings.Repeat("a", 64), now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var cacheCount, historyCount int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_cache WHERE source_id = 'legacy-catalog-v2'`).Scan(&cacheCount); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_manifest_history WHERE source_id = 'legacy-catalog-v2'`).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if cacheCount != 0 || historyCount != 1 {
		t.Fatalf("legacy cache=%d immutable history=%d", cacheCount, historyCount)
	}
	version, err := sqliteSchemaVersion(ctx, migrated.db)
	if err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestVersion52MigrationPreservesBuiltinHeadscaleCloudflareDNS(t *testing.T) {
	directory := t.TempDir()
	store := legacyMigrationStore(t, directory, 51)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at)
		VALUES('headscale', 'builtin', 'https://headscale.example.com', NULL, 'failed', ?, ?);
		DELETE FROM settings WHERE key IN ('headscale_dns_policy', 'headscale_dns_resolvers')`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	policy, resolvers, err := migrated.builtinHeadscaleDNSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy != "custom" || len(resolvers) != 2 || resolvers[0] != "1.1.1.1" || resolvers[1] != "1.0.0.1" {
		t.Fatalf("migrated Headscale DNS = %q %#v", policy, resolvers)
	}
}

func TestVersion54MigrationPreservesAssistantProposalExecution(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 53); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO admins(id, username, password_hash, created_at) VALUES('admin-v53', 'admin-v53', 'hash', ?);
		INSERT INTO assistant_conversations(id, admin_id, title, created_at, updated_at) VALUES('conversation-v53', 'admin-v53', 'Legacy proposal', ?, ?);
		INSERT INTO assistant_runs(id, conversation_id, admin_id, status, created_at, updated_at) VALUES('run-v53', 'conversation-v53', 'admin-v53', 'approval_required', ?, ?);
		INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, created_at, updated_at, application_id) VALUES('deployment-v53', 'agent-v3', 'legacy/app', '1.0.0', '{}', '{}', 'configure', 'succeeded', ?, ?, 'application-v3');
		INSERT INTO change_proposals(id, conversation_id, run_id, admin_id, kind, request_json, summary_json, digest, targets_json, expected_revision, policy_version, risk, status, expires_at, deployment_id, created_at, updated_at)
		VALUES('proposal-v53', 'conversation-v53', 'run-v53', 'admin-v53', 'install_application', '{}', '{}', 'digest-v53', '[]', 'revision-v53', 'install-application-v1', 'medium', 'applied', ?, 'deployment-v53', ?, ?)`, now, now, now, now, now, now, now, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 54); err != nil {
		t.Fatal(err)
	}
	var kind, deploymentID, executionID string
	if err := db.QueryRowContext(ctx, `SELECT kind, deployment_id, execution_id FROM change_proposals WHERE id = 'proposal-v53'`).Scan(&kind, &deploymentID, &executionID); err != nil {
		t.Fatal(err)
	}
	if kind != "install_application" || deploymentID != "deployment-v53" || executionID != deploymentID {
		t.Fatalf("migrated proposal = kind %q deployment %q execution %q", kind, deploymentID, executionID)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO change_proposals(id, conversation_id, run_id, admin_id, kind, request_json, summary_json, digest, targets_json, expected_revision, policy_version, risk, status, expires_at, execution_id, created_at, updated_at)
		VALUES('rotation-proposal-v54', 'conversation-v53', 'run-v53', 'admin-v53', 'rotate_cpa_credential', '{}', '{}', 'rotation-digest', '[]', 'rotation-revision', 'rotate-cpa-credential-v1', 'high', 'pending', ?, '', ?, ?)`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), now, now); err != nil {
		t.Fatalf("v54 schema rejected a CPA rotation proposal: %v", err)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v54 migration left a foreign-key violation")
	}
}

func TestVersion46MigrationRetiresSharedPathPublications(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 45); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE publications
		SET kind = 'cloudflare_tunnel', hostname = 'service-vastora.example.test', path_prefix = '/s/legacy',
			dns_provider = 'cloudflare', dns_record_id = 'shared-dns', access_application_id = 'shared-access'
		WHERE id = 'publication-v3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE routes
		SET hostname = 'service-vastora.example.test', path_prefix = '/s/legacy'
		WHERE id = 'route-v3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 46); err != nil {
		t.Fatal(err)
	}

	var status string
	var cleanupPending, desiredRevision int
	if err := db.QueryRowContext(ctx, `SELECT status, cleanup_pending, desired_revision FROM publications WHERE id = 'publication-v3'`).Scan(&status, &cleanupPending, &desiredRevision); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || cleanupPending != 1 || desiredRevision != 2 {
		t.Fatalf("retired publication status=%q cleanup=%d revision=%d", status, cleanupPending, desiredRevision)
	}
	var routes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE id = 'route-v3'`).Scan(&routes); err != nil || routes != 0 {
		t.Fatalf("retired routes=%d err=%v", routes, err)
	}
	var gatewayID string
	if err := db.QueryRowContext(ctx, `SELECT gateway_node_id FROM retired_shared_publication_gateways`).Scan(&gatewayID); err != nil || gatewayID != "agent-v3" {
		t.Fatalf("retired gateway=%q err=%v", gatewayID, err)
	}
	for _, table := range []string{"publications", "routes"} {
		var pathColumns int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'path_prefix'`, table).Scan(&pathColumns); err != nil || pathColumns != 0 {
			t.Fatalf("%s path columns=%d err=%v", table, pathColumns, err)
		}
	}
}

func TestVersion47MigrationConvertsInboundPlansToMonthlyBillingDay(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 46); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	boundary := "2026-09-22T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(
		service_id, inbound_tag, total_bytes, reset_days, next_reset_at, revision, status, updated_at
	) VALUES('service-v3', 'legacy-inbound', 107374182400, 30, ?, 2, 'active', ?)`, boundary, now); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 47); err != nil {
		t.Fatal(err)
	}
	var resetDay int
	var nextResetAt string
	if err := db.QueryRowContext(ctx, `SELECT reset_day, next_reset_at FROM three_x_ui_inbound_plans WHERE service_id = 'service-v3'`).Scan(&resetDay, &nextResetAt); err != nil {
		t.Fatal(err)
	}
	if resetDay != 1 || nextResetAt != boundary {
		t.Fatalf("monthly plan reset day=%d next=%q", resetDay, nextResetAt)
	}
	var oldColumn int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('three_x_ui_inbound_plans') WHERE name = 'reset_days'`).Scan(&oldColumn); err != nil || oldColumn != 0 {
		t.Fatalf("legacy reset_days column=%d err=%v", oldColumn, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET reset_day = 32 WHERE service_id = 'service-v3'`); err == nil {
		t.Fatal("monthly reset day accepted a value above 31")
	}
}
