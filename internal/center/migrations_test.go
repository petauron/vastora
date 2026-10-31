package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
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
	createCurrentBusinessFixture(t, migratedDirectory)
	old, err := Open(migratedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct schema 122 before the tables introduced by migrations 125, 127 and 128.
	if _, err := old.db.Exec(`DROP TABLE node_site_certmagic_storage; DROP TABLE node_site_tasks; DROP TABLE node_sites; DROP TABLE tailscale_update_tasks; DROP TABLE os_reinstall_tasks; DELETE FROM goose_db_version; INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,1),(122,1); PRAGMA user_version=122;`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('baseline-preserved',X'0001FF','before','before')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO agent_removals(agent_id,state,prepared,headscale_done,headscale_identity_json,tunnel_done,last_error,created_at,updated_at) VALUES('agent-v3','failed',0,1,'{"nodeKey":"synthetic-key"}',0,'historical pilot fence','before','before')`); err != nil {
		t.Fatal(err)
	}
	// Compare every business table, including opaque encrypted bytes. Only the
	// migration journal is expected to change.
	snapshot := func(db *sql.DB) map[string][]string {
		t.Helper()
		result := map[string][]string{}
		for name := range databaseSchemaShape(t, db) {
			if name == "goose_db_version" {
				continue
			}
			rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				encoded, err := json.Marshal(values)
				if err != nil {
					rows.Close()
					t.Fatal(err)
				}
				result[name] = append(result[name], string(encoded))
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
			sort.Strings(result[name])
		}
		return result
	}
	before := snapshot(old.db)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(migratedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	if !reflect.DeepEqual(before, snapshot(migrated.db)) {
		t.Fatal("baseline conversion changed business data")
	}
	var count int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM goose_db_version`).Scan(&count); err != nil || count != int(centerSchemaVersion-schemaBaselineVersion+2) {
		t.Fatalf("history not compacted: %d %v", count, err)
	}
	backups, err := filepath.Glob(filepath.Join(migratedDirectory, "migration-backups", "center-v122-before-v123-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing baseline backup: %v %v", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if version, err := sqliteSchemaVersion(context.Background(), backup); err != nil || version != 122 {
		t.Fatalf("backup marker: %d %v", version, err)
	}
	if !reflect.DeepEqual(before, snapshot(backup)) {
		t.Fatal("backup lost business data")
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err = Open(migratedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	again, err := filepath.Glob(filepath.Join(migratedDirectory, "migration-backups", "center-v122-before-v123-*.db"))
	if err != nil || len(again) != 1 {
		t.Fatal("baseline conversion repeated", err)
	}
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

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "predates baseline 123") {
		t.Fatalf("regressed current-schema marker was accepted: %v", err)
	}
}

func TestOpenRejectsInconsistentBaselineHistory(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 122`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "baseline requires completed schema 122") {
		t.Fatalf("expected fail-closed schema marker validation, got %v", err)
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

func createCurrentBusinessFixture(t *testing.T, directory string) {
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

}
