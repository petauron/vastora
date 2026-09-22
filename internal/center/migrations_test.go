package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
)

type schemaColumn struct {
	Name, Type, Default string
	NotNull, PrimaryKey int
}

type schemaIndex struct {
	Name            string
	Unique, Partial int
	Columns         []string
}

type schemaTable struct {
	Columns []schemaColumn
	Indexes []schemaIndex
}

func TestFreshAndMigratedDatabasesHaveEquivalentSchema(t *testing.T) {
	fresh, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	migratedDirectory := t.TempDir()
	createLegacyVersion3Database(t, migratedDirectory)
	migrated, err := Open(migratedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	freshShape := databaseSchemaShape(t, fresh.db)
	migratedShape := databaseSchemaShape(t, migrated.db)
	if !reflect.DeepEqual(freshShape, migratedShape) {
		t.Fatalf("fresh and migrated schema differ:\nfresh=%#v\nmigrated=%#v", freshShape, migratedShape)
	}
}

func TestOpenRejectsCurrentSchemaWithRegressedVersionMarker(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 79`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "migrated SQLite schema is version 79") {
		t.Fatalf("regressed current-schema marker was accepted: %v", err)
	}
}

func TestOpenRejectsIncompleteReleasedVersion80Migration(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `DROP TABLE node_diagnostic_checks; PRAGMA user_version = 79`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "migrated SQLite schema is version 79") {
		t.Fatalf("expected fail-closed schema marker validation, got %v", err)
	}
}

func TestVersion81MigrationAddsHostProfileDiagnostics(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE node_diagnostic_checks`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, nodeDiagnosticsSchema80SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 80`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id >= 81`); err != nil {
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
	var schema string
	if err := migrated.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='node_diagnostic_checks'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schema, "'node.host-profile'") {
		t.Fatalf("host profile kind missing after migration: %s", schema)
	}
}

func TestVersion83MigrationMovesManagedRealityToLocalDockerAlias(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	siteID := testSiteID(t, store)
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: siteID, Name: "Worker", CenterURL: "https://center.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,runtime_generation,created_at,updated_at) VALUES('worker-app','Proxy',?,?,'vastora-official/3x-ui','','running','docker','worker',2,?,?)`, node.ID, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,observed_listen,status,created_at,updated_at) VALUES('worker-service','worker-app',?,'inbound-9','tcp',443,443,'100.64.0.10:443','observed','vless/tcp/reality','100.64.0.10','ready',?,?)`, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	desired := `{"revision":4,"nodeId":"` + node.ID + `","listener":{"address":"203.0.113.10","port":443,"caddyAddress":"","caddyPort":0,"rejectUnmatched":true,"routes":[{"id":"managed","hostname":"reality.example.test","applicationNodeId":"` + node.ID + `","managedReality":true,"proxyProtocol":"v2","upstreams":[{"address":"100.64.0.10","port":443}]},{"id":"ordinary","hostname":"ordinary.example.test","upstreams":[{"address":"service","port":8443}]}]}}`
	if _, err := store.db.ExecContext(ctx, `INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,lease_expires_at,last_error,updated_at) VALUES(?,4,4,?,'ready',3,'lease','old error',?)`, node.ID, desired, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 82; DELETE FROM goose_db_version WHERE version_id >= 83`); err != nil {
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
	var revision, applied, attempt int64
	var raw, status, lease, lastError string
	if err := migrated.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,attempt,CAST(desired_json AS TEXT),status,lease_expires_at,last_error FROM node_listener_states WHERE node_id=?`, node.ID).Scan(&revision, &applied, &attempt, &raw, &status, &lease, &lastError); err != nil {
		t.Fatal(err)
	}
	var state gateway.NodeListenerState
	if json.Unmarshal([]byte(raw), &state) != nil || len(state.Listener.Routes) != 2 {
		t.Fatalf("invalid migrated listener state: %s", raw)
	}
	managed, ordinary := state.Listener.Routes[0], state.Listener.Routes[1]
	if revision != 6 || state.Revision != 6 || applied != 4 || attempt != 0 || status != "pending" || lease != "" || lastError != "" {
		t.Fatalf("migration state = revision %d/%d applied %d attempt %d status %q lease %q error %q", revision, state.Revision, applied, attempt, status, lease, lastError)
	}
	if len(managed.Upstreams) != 1 || managed.Upstreams[0].Address != dockerruntime.XrayAlias || managed.Upstreams[0].Port != 443 || len(ordinary.Upstreams) != 1 || ordinary.Upstreams[0].Address != "service" || ordinary.Upstreams[0].Port != 8443 {
		t.Fatalf("migration routes = %#v", state.Listener.Routes)
	}
	var endpoint, observedListen string
	if err := migrated.db.QueryRowContext(ctx, `SELECT endpoint,observed_listen FROM services WHERE id='worker-service'`).Scan(&endpoint, &observedListen); err != nil || endpoint != dockerruntime.XrayAlias+":443" || observedListen != "100.64.0.10" {
		t.Fatalf("migrated service endpoint=%q observed=%q err=%v", endpoint, observedListen, err)
	}
}

func TestVersion57MigrationSelectsOneGlobalThreeXUIControllerAndQueuesLegacyConvergence(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 56); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	older := now.Add(-time.Hour).Format(time.RFC3339Nano)
	newer := now.Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO sites(id, organization_id, name, code, timezone, status, created_at, updated_at) VALUES
			('global-site-a', '` + defaultOrganizationID + `', 'A', 'global-a', 'UTC', 'active', '` + older + `', '` + older + `'),
			('global-site-b', '` + defaultOrganizationID + `', 'B', 'global-b', 'UTC', 'active', '` + newer + `', '` + newer + `')`,
		`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES
			('global-agent-a', 'A', X'5701', '0.1.0-alpha.1', 'active', '` + older + `', '` + newer + `', 'global-site-a'),
			('global-agent-b', 'B', X'5702', '0.1.0-alpha.1', 'active', '` + newer + `', '` + newer + `', 'global-site-b')`,
		`INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES
			('global-controller-a', '3x-ui A', 'global-agent-a', 'global-site-a', '` + threeXUIAppKey + `', 'example/3x-ui', 'running', 'docker', 'master', '` + older + `', '` + newer + `'),
			('global-controller-b', '3x-ui B', 'global-agent-b', 'global-site-b', '` + threeXUIAppKey + `', 'example/3x-ui', 'running', 'docker', 'master', '` + newer + `', '` + newer + `')`,
		`INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, management, status, created_at, updated_at)
			VALUES('global-panel-b', 'global-controller-b', 'global-site-b', 'panel', 'http', 2053, 2053, '10.0.0.2:2053', 'catalog', 1, 'ready', '` + newer + `', '` + newer + `')`,
		`INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, status, created_at, updated_at)
			VALUES('global-panel-publication-b', 'global-panel-b', 'lan_gateway', 'site_gateway', 'global-agent-b', 'panel.example.test', 'manual', 'ready', '` + newer + `', '` + newer + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
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
	var controllerID string
	if err := migrated.db.QueryRowContext(ctx, `SELECT controller_application_id FROM three_x_ui_control_plane WHERE id = 1`).Scan(&controllerID); err != nil || controllerID != "global-controller-b" {
		t.Fatalf("global controller = %q, err = %v", controllerID, err)
	}
	var kind, sourceID, targetID, state, step string
	if err := migrated.db.QueryRowContext(ctx, `SELECT kind, source_application_id, target_application_id, state, step FROM three_x_ui_migrations WHERE kind = 'consolidate'`).Scan(&kind, &sourceID, &targetID, &state, &step); err != nil {
		t.Fatal(err)
	}
	if kind != "consolidate" || sourceID != "global-controller-a" || targetID != "global-controller-b" || state != "backing_up" || step != "backup" {
		t.Fatalf("automatic convergence = kind=%q source=%q target=%q state=%q step=%q", kind, sourceID, targetID, state, step)
	}
	var queued int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id = 'global-controller-a' AND kind = '3xui.controller.manage' AND state = 'pending'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued restore point commands = %d, err = %v", queued, err)
	}
	if _, err := migrated.db.ExecContext(ctx, `UPDATE applications SET status = 'running' WHERE id = 'global-controller-a'`); err != nil {
		t.Fatalf("legacy controller self-update was blocked: %v", err)
	}
	if _, err := migrated.db.ExecContext(ctx, `UPDATE applications SET app_key = ?, role = 'master', status = 'running' WHERE id = 'application-v3'`, threeXUIAppKey); err == nil {
		t.Fatal("global controller guard accepted a second new controller")
	}
}

func TestVersion56MigrationSeparatesNodeDirectIngressAndFailsClosedOnCrossNodeRows(t *testing.T) {
	directory := t.TempDir()
	store := legacyMigrationStore(t, directory, 55)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO sites(id, organization_id, name, code, timezone, status, created_at, updated_at) VALUES('site-v55', '` + defaultOrganizationID + `', 'Legacy', 'legacy-v55', 'UTC', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json) VALUES('node-v55-a', 'A', X'5601', '0.1.0-alpha.1', 'active', '` + now + `', '` + now + `', 'site-v55', '["worker","gateway"]', '{"docker":true,"gateway":true,"tunnel":true}'), ('node-v55-b', 'B', X'5602', '0.1.0-alpha.1', 'active', '` + now + `', '` + now + `', 'site-v55', '["worker"]', '{"docker":true}'), ('node-v55-c', 'C', X'5603', '0.1.0-alpha.1', 'active', '` + now + `', '` + now + `', 'site-v55', '["worker"]', '{"docker":true}')`,
		`INSERT INTO agent_network_profiles(agent_id, service_address, public_address, public_bind_address, public_mode, enabled_kinds_json, direct_public, public_verified_at, confirmed_at, candidate_observed_at) VALUES('node-v55-a', '10.0.0.10', '203.0.113.10', '203.0.113.10', 'direct', '["public"]', 1, '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO site_gateways(site_id, agent_id, created_at) VALUES('site-v55', 'node-v55-a', '` + now + `')`,
		`INSERT INTO gateway_components(gateway_node_id, desired_status, generation, applied_generation, status, updated_at) VALUES('node-v55-a', 'running', 1, 1, 'ready', '` + now + `')`,
		`INSERT INTO applications(id, name, node_id, site_id, app_key, status, runtime, created_at, updated_at) VALUES('app-v55', '3x-ui', 'node-v55-a', 'site-v55', 'vastora-official/3x-ui', 'running', 'docker', '` + now + `', '` + now + `'), ('app-v55-unready', '3x-ui C', 'node-v55-c', 'site-v55', 'vastora-official/3x-ui', 'running', 'docker', '` + now + `', '` + now + `'), ('app-v55-web', 'Web', 'node-v55-a', 'site-v55', 'test/web', 'running', 'systemd', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at) VALUES('service-v55-valid', 'app-v55', 'site-v55', 'valid', 'tcp', 443, 443, '10.0.0.10:443', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `'), ('service-v55-cross', 'app-v55', 'site-v55', 'cross', 'tcp', 2443, 2443, '10.0.0.10:2443', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `'), ('service-v55-unready', 'app-v55-unready', 'site-v55', 'unready', 'tcp', 443, 443, '10.0.0.30:443', 'observed', 'vless/tcp/reality', 'ready', '` + now + `', '` + now + `'), ('service-v55-web', 'app-v55-web', 'site-v55', 'web', 'http', 8080, 8080, '10.0.0.10:8080', 'observed', 'http', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, companion_tag, status, created_at, updated_at) VALUES('service-v55-valid', 'www.example.com:443', '203.0.113.34', 'www.example.com', 'valid-guard', 'ready', '` + now + `', '` + now + `'), ('service-v55-unready', 'www.example.com:443', '203.0.113.34', 'www.example.com', 'unready-guard', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at) VALUES('publication-v55-valid', 'service-v55-valid', 'public_shared_443', 'node-v55-a', 'valid.example.test', 'www.example.com', 'manual', 7, 7, 'ready', '` + now + `', '` + now + `'), ('publication-v55-cross', 'service-v55-cross', 'public_shared_443', 'node-v55-b', 'cross.example.test', 'www.example.com', 'manual', 4, 4, 'ready', '` + now + `', '` + now + `'), ('publication-v55-unready', 'service-v55-unready', 'public_shared_443', 'node-v55-c', 'unready.example.test', 'www.example.com', 'manual', 2, 2, 'ready', '` + now + `', '` + now + `'), ('publication-v55-tunnel', 'service-v55-web', 'cloudflare_tunnel', 'node-v55-a', 'tunnel.example.test', '', 'cloudflare', 9, 9, 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, desired_revision, applied_revision, status, created_at, updated_at) VALUES('route-v55-tunnel', 'publication-v55-tunnel', 'site-v55', 'service-v55-web', 'node-v55-a', 'tunnel.example.test', 'http', '["10.0.0.10:8080"]', 9, 9, 'ready', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var owner, entryNode, status, lastError string
	var actionRequired int
	if err := migrated.db.QueryRowContext(ctx, `SELECT ingress_owner, entry_node_id, status, action_required FROM publications WHERE id = 'publication-v55-valid'`).Scan(&owner, &entryNode, &status, &actionRequired); err != nil {
		t.Fatal(err)
	}
	if owner != ingressApplicationNode || entryNode != "node-v55-a" || status != "pending" || actionRequired != 0 {
		t.Fatalf("valid migration = owner:%q entry:%q status:%q action:%d", owner, entryNode, status, actionRequired)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT ingress_owner, entry_node_id, status, action_required, last_error FROM publications WHERE id = 'publication-v55-cross'`).Scan(&owner, &entryNode, &status, &actionRequired, &lastError); err != nil {
		t.Fatal(err)
	}
	if owner != ingressApplicationNode || entryNode != "node-v55-a" || status != "stopped" || actionRequired != 1 || !strings.Contains(lastError, "node-direct entry") {
		t.Fatalf("cross-node migration = owner:%q entry:%q status:%q action:%d error:%q", owner, entryNode, status, actionRequired, lastError)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT ingress_owner, entry_node_id, status, action_required, last_error FROM publications WHERE id = 'publication-v55-unready'`).Scan(&owner, &entryNode, &status, &actionRequired, &lastError); err != nil {
		t.Fatal(err)
	}
	if owner != ingressApplicationNode || entryNode != "node-v55-c" || status != "stopped" || actionRequired != 1 || !strings.Contains(lastError, "confirmed public network profile") {
		t.Fatalf("unready migration = owner:%q entry:%q status:%q action:%d error:%q", owner, entryNode, status, actionRequired, lastError)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT ingress_owner, entry_node_id, status FROM publications WHERE id = 'publication-v55-tunnel'`).Scan(&owner, &entryNode, &status); err != nil {
		t.Fatal(err)
	}
	if owner != ingressTunnelConnector || entryNode != "node-v55-a" || status != "pending" {
		t.Fatalf("Tunnel migration = owner:%q entry:%q status:%q", owner, entryNode, status)
	}
	var tunnelCutovers int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tunnel_connector_migration_cutovers WHERE publication_id = 'publication-v55-tunnel' AND legacy_gateway_id = 'node-v55-a' AND connector_reconciled = 0`).Scan(&tunnelCutovers); err != nil || tunnelCutovers != 1 {
		t.Fatalf("pending Tunnel connector cutover = %d, err = %v", tunnelCutovers, err)
	}
	var queued int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_states WHERE node_id = 'node-v55-a' AND status = 'pending'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("migrated node listener queue = %d, err = %v", queued, err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_states WHERE node_id = 'node-v55-c'`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("unready migration queued an unusable listener = %d, err = %v", queued, err)
	}
	var cutovers int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_migration_cutovers`).Scan(&cutovers); err != nil || cutovers != 2 {
		t.Fatalf("pending node-listener migration cutovers = %d, err = %v", cutovers, err)
	}
	var marker string
	if err := migrated.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker); err != nil || marker != "applying" {
		t.Fatalf("node-listener migration marker = %q, err = %v", marker, err)
	}
	var revision int64
	if err := migrated.db.QueryRowContext(ctx, `SELECT desired_revision FROM node_listener_states WHERE node_id = 'node-v55-a'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.db.ExecContext(ctx, `UPDATE node_listener_states SET status = 'applying', attempt = 1 WHERE node_id = 'node-v55-a'`); err != nil {
		t.Fatal(err)
	}
	if err := migrated.completeNodeListenerState(ctx, commitProjectionOnlyForTest, "node-v55-a", revision, 1, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_listener_migration_cutovers`).Scan(&cutovers); err != nil || cutovers != 1 {
		t.Fatalf("legacy ingress retired before external replacement verification: %d, err = %v", cutovers, err)
	}
	if _, err := migrated.markPublicationReady(ctx, "publication-v55-valid", 7); err != nil {
		t.Fatal(err)
	}
	var markerTables int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'node_listener_migration_cutovers'`).Scan(&markerTables); err != nil || markerTables != 0 {
		t.Fatalf("completed node-listener migration cutover tables = %d, err = %v", markerTables, err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, nodeListenerMigrationSetting).Scan(&marker); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed node-listener migration marker = %q, err = %v", marker, err)
	}
	if err := migrated.completeTunnelConnectorMigrationReconcile(ctx, "publication-v55-tunnel"); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.markPublicationReady(ctx, "publication-v55-tunnel", 9); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE id = 'route-v55-tunnel'`).Scan(&tunnelCutovers); err != nil || tunnelCutovers != 0 {
		t.Fatalf("retired Tunnel connector Caddy route = %d, err = %v", tunnelCutovers, err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tunnel_connector_migration_cutovers'`).Scan(&markerTables); err != nil || markerTables != 0 {
		t.Fatalf("completed Tunnel connector migration table = %d, err = %v", markerTables, err)
	}
}

func databaseSchemaShape(t *testing.T, db *sql.DB) map[string]schemaTable {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	shape := make(map[string]schemaTable, len(tables))
	for _, tableName := range tables {
		value := schemaTable{}
		columnRows, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?) ORDER BY cid`, tableName)
		if err != nil {
			t.Fatal(err)
		}
		for columnRows.Next() {
			var column schemaColumn
			if err := columnRows.Scan(&column.Name, &column.Type, &column.NotNull, &column.Default, &column.PrimaryKey); err != nil {
				t.Fatal(err)
			}
			value.Columns = append(value.Columns, column)
		}
		if err := columnRows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Slice(value.Columns, func(i, j int) bool { return value.Columns[i].Name < value.Columns[j].Name })
		indexRows, err := db.Query(`SELECT name, "unique", partial FROM pragma_index_list(?)`, tableName)
		if err != nil {
			t.Fatal(err)
		}
		indexes := []schemaIndex{}
		for indexRows.Next() {
			var index schemaIndex
			if err := indexRows.Scan(&index.Name, &index.Unique, &index.Partial); err != nil {
				t.Fatal(err)
			}
			indexes = append(indexes, index)
		}
		if err := indexRows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, index := range indexes {
			columnNames, err := db.Query(`SELECT COALESCE(name, '<expression>') FROM pragma_index_info(?) ORDER BY seqno`, index.Name)
			if err != nil {
				t.Fatal(err)
			}
			for columnNames.Next() {
				var name string
				if err := columnNames.Scan(&name); err != nil {
					t.Fatal(err)
				}
				index.Columns = append(index.Columns, name)
			}
			if err := columnNames.Close(); err != nil {
				t.Fatal(err)
			}
			value.Indexes = append(value.Indexes, index)
		}
		sort.Slice(value.Indexes, func(i, j int) bool { return value.Indexes[i].Name < value.Indexes[j].Name })
		shape[tableName] = value
	}
	return shape
}

func legacyMigrationStore(t *testing.T, directory string, version int64) *Store {
	t.Helper()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := &Store{db: db}
	ctx := context.Background()
	if err := store.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, version); err != nil {
		t.Fatal(err)
	}
	return store
}

func createLegacyVersion3Database(t *testing.T, directory string) {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO sites(id, organization_id, name, code, timezone, status, created_at, updated_at) VALUES('site-v3', '` + defaultOrganizationID + `', 'Legacy', 'legacy', 'UTC', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('agent-v3', 'Legacy Agent', X'0102', '0.1.0-alpha.1', 'active', '` + now + `', '` + now + `', 'site-v3')`,
		`INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at) VALUES('application-v3', 'Legacy App', 'agent-v3', 'site-v3', 'legacy/app', 'running', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, status, created_at, updated_at) VALUES('service-v3', 'application-v3', 'site-v3', 'manager', 'http', 8080, 8080, '10.0.0.2:8080', 'catalog', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, status, created_at, updated_at) VALUES('publication-v3', 'service-v3', 'lan_gateway', 'site_gateway', 'agent-v3', 'legacy.example.test', 'manual', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, status, created_at, updated_at) VALUES('route-v3', 'publication-v3', 'site-v3', 'service-v3', 'agent-v3', 'legacy.example.test', 'http', '[]', 'ready', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DROP TABLE meridian_deployments`,
		`DROP TABLE meridian_usage_watermarks`,
		`DROP TABLE meridian_route_grants`,
		`DROP TABLE meridian_credentials`,
		`DROP TABLE meridian_accounts`,
		`DROP TABLE meridian_endpoints`,
		`DROP TABLE meridian_cutover`,
		`DROP TABLE xray_configuration_recoveries`,
		`DROP TABLE node_diagnostic_checks`,
		`DROP TABLE ip_quality_checks`,
		`DROP TABLE agent_removals`,
		`DROP TABLE execution_events`,
		`DROP TABLE execution_claim_control_events`,
		`DROP TABLE task_executions`,
		`DROP TABLE agent_execution_sessions`,
		`DROP TABLE agent_execution_session_history`,
		`DROP TABLE landing_client_blocks`,
		`DROP TABLE landing_client_grants`,
		`DROP TABLE landing_client_capabilities`,
		`DROP TABLE three_x_ui_client_accounts`,
		`DROP TABLE three_x_ui_node_protocols`,
		`DROP TABLE official_catalog_trust`,
		`DROP TABLE cloudflare_access_settings`,
		`DROP TABLE reality_security_checks`,
		`DROP TABLE node_listener_states`,
		`DROP TABLE landing_proxy_retirements`,
		`DROP TABLE landing_server_states`,
		`DROP TABLE landing_proxy_states`,
		`DROP TABLE assistant_audit_events`,
		`DROP TABLE assistant_events`,
		`DROP TABLE change_approvals`,
		`DROP TABLE change_proposals`,
		`DROP TABLE assistant_tool_calls`,
		`DROP TABLE assistant_runs`,
		`DROP TABLE assistant_messages`,
		`DROP TABLE assistant_conversations`,
		`DROP TABLE assistant_model_providers`,
		`DROP TABLE recovery_evidence`,
		`DROP TABLE agent_network_profile_recovery`,
		`ALTER TABLE agents DROP COLUMN runtime_recovery`,
		`DROP INDEX deployments_change_proposal_idx`,
		`DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment`,
		`DROP TRIGGER agent_enrollment_operation_secret_cleanup`,
		`DROP TRIGGER application_commands_block_during_three_x_ui_deployment`,
		`DROP TRIGGER deployments_block_during_three_x_ui_data_plane`,
		`DROP TRIGGER application_commands_block_during_three_x_ui_migration`,
		`DROP TRIGGER application_command_updates_block_during_three_x_ui_migration`,
		`DROP TRIGGER deployments_block_during_three_x_ui_migration`,
		`DROP TABLE three_x_ui_inbound_plans`,
		`DROP TABLE site_certificates`,
		`DROP TABLE three_x_ui_control_plane`,
		`DROP TABLE three_x_ui_migrations`,
		`DROP TABLE three_x_ui_backups`,
		`DROP TABLE three_x_ui_nodes`,
		`DROP TABLE headscale_api_keys`,
		`DROP TABLE application_credential_rotations`,
		`DROP TABLE initial_setup_operations`,
		`DROP TABLE cloudflare_tunnel_operations`,
		`DROP TABLE agent_enrollment_operations`,
		`DROP TABLE system_endpoint_aliases`,
		`DROP TABLE login_failures`,
		`DROP TABLE center_remote_access`,
		`DROP TABLE catalog_manifest_history`,
		`DROP TABLE agent_updates`,
		`DROP TABLE agent_decommissions`,
		`DROP TRIGGER applications_one_global_three_x_ui_master_insert`,
		`DROP TRIGGER applications_one_global_three_x_ui_master_update`,
		`ALTER TABLE agents DROP COLUMN operating_system`,
		`ALTER TABLE agents DROP COLUMN architecture`,
		`ALTER TABLE agents DROP COLUMN runtime_generation`,
		`ALTER TABLE agents DROP COLUMN tailscale_ownership`,
		`ALTER TABLE agents DROP COLUMN x25519_public_key`,
		`ALTER TABLE agents DROP COLUMN credential_revoked_at`,
		`ALTER TABLE agents DROP COLUMN remote_update_supported`,
		`ALTER TABLE agents DROP COLUMN public_egress_observed_at`,
		`ALTER TABLE agents DROP COLUMN public_egress_mode`,
		`ALTER TABLE agents DROP COLUMN public_egress_bind_address`,
		`ALTER TABLE agents DROP COLUMN public_egress_address`,
		`ALTER TABLE agent_network_profiles DROP COLUMN public_verified_at`,
		`ALTER TABLE agent_network_profiles DROP COLUMN public_mode`,
		`ALTER TABLE agent_network_profiles DROP COLUMN public_bind_address`,
		`ALTER TABLE applications DROP COLUMN role`,
		`ALTER TABLE applications DROP COLUMN runtime_generation`,
		`ALTER TABLE deployments DROP COLUMN executed_runtime_generation`,
		`ALTER TABLE deployments DROP COLUMN runtime_generation`,
		`ALTER TABLE deployments DROP COLUMN reconciliation_requested`,
		`ALTER TABLE application_commands DROP COLUMN reconciliation_requested`,
		`DROP INDEX agent_enrollment_one_reconnect_idx`,
		`ALTER TABLE agent_enrollment_tokens DROP COLUMN target_agent_id`,
		`ALTER TABLE agent_enrollment_tokens DROP COLUMN ca_certificate_pem`,
		`ALTER TABLE deployments DROP COLUMN registry_credential_id`,
		`ALTER TABLE deployments DROP COLUMN change_proposal_id`,
		`ALTER TABLE deployments DROP COLUMN pre_dispatch_application_status`,
		`ALTER TABLE services DROP COLUMN region_code`,
		`ALTER TABLE services DROP COLUMN display_name`,
		`ALTER TABLE catalog_sources DROP COLUMN last_checked_at`,
		`ALTER TABLE catalog_sources DROP COLUMN generation`,
		`ALTER TABLE catalog_sources DROP COLUMN revision`,
		`DROP TABLE application_commands`,
		`DROP TABLE certificate_authorities`,
		`CREATE TABLE publications_v3 (
			id TEXT PRIMARY KEY, service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'cloudflare_tunnel')),
			gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT, hostname TEXT NOT NULL,
			dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
			dns_record_id TEXT NOT NULL DEFAULT '', tls_enabled INTEGER NOT NULL DEFAULT 0,
			desired_revision INTEGER NOT NULL DEFAULT 1, applied_revision INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
			last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(service_id, kind, hostname))`,
		`INSERT INTO publications_v3(id, service_id, kind, gateway_node_id, hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at)
		 SELECT id, service_id, kind, entry_node_id, hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at FROM publications`,
		`CREATE TABLE routes_v3 (
			id TEXT PRIMARY KEY, publication_id TEXT NOT NULL REFERENCES publications_v3(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			hostname TEXT NOT NULL, protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https')),
			upstreams_json BLOB NOT NULL, tls_enabled INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed')),
			desired_revision INTEGER NOT NULL DEFAULT 0, applied_revision INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(publication_id, gateway_node_id))`,
		`INSERT INTO routes_v3(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, desired_revision, applied_revision, last_error, created_at, updated_at)
		 SELECT id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, desired_revision, applied_revision, last_error, created_at, updated_at FROM routes`,
		`DROP TABLE routes`,
		`DROP TABLE publications`,
		`ALTER TABLE publications_v3 RENAME TO publications`,
		`ALTER TABLE routes_v3 RENAME TO routes`,
		`DROP TABLE goose_db_version`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("prepare legacy schema: %v\n%s", err, statement)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO publications(
		id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at
	) VALUES('unsupported-v3', 'service-v3', 'public_shared_443', 'agent-v3', 'unsupported.example.test', 'manual', 'pending', ?, ?)`, now, now); err == nil {
		t.Fatal("legacy publications constraint unexpectedly accepted public_shared_443")
	}
}
