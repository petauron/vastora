package center

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion102MeridianHostMigration(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyMigrationStore(t, directory, 101)
			const stamp = "2026-01-01T00:00:00Z"
			for _, query := range []string{
				`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('host-key',X'010203','` + stamp + `','` + stamp + `')`,
				`INSERT INTO agent_network_profiles(agent_id,service_address,headscale_address,enabled_kinds_json,confirmed_at,candidate_observed_at) VALUES('agent-v3','100.64.0.23','100.64.0.23','["headscale"]','` + stamp + `','` + stamp + `')`,
				`INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
    VALUES('host-endpoint','application-v3','service-v3','host-entry',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]','host-key','test-public-key','["abcd"]',4,4,1,'ready','` + stamp + `','` + stamp + `')`,
			} {
				if _, err := legacy.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if busy {
				if _, err := legacy.db.Exec(`INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES('host-command','application-v3','site-v3','Legacy App','agent-v3','agent-v3','meridian.runtime.apply','{}','running',?,?)`, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacy.db.Close(); err != nil {
				t.Fatal(err)
			}
			migrated, err := Open(directory)
			if busy {
				if err == nil {
					migrated.Close()
					t.Fatal("accepted outstanding runtime task")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer migrated.Close()
				var address, advertise, status, service string
				var port, advertisePort, desired, applied, healthy int
				if err := migrated.db.QueryRow(`SELECT listen_address,listen_port,advertise_host,advertise_port,desired_revision,applied_revision,runtime_healthy,status FROM meridian_endpoints WHERE id='host-endpoint'`).Scan(&address, &port, &advertise, &advertisePort, &desired, &applied, &healthy, &status); err != nil {
					t.Fatal(err)
				}
				if address != "100.64.0.23" || port != 10443 || advertise != "entry.example.test" || advertisePort != 443 || desired != 5 || applied != 4 || healthy != 0 || status != "pending" {
					t.Fatalf("unsafe migrated endpoint: %s %d %s %d %d/%d %d %s", address, port, advertise, advertisePort, desired, applied, healthy, status)
				}
				if err := migrated.db.QueryRow(`SELECT endpoint FROM services WHERE id='service-v3'`).Scan(&service); err != nil || service != "10.0.0.2:8080" {
					t.Fatalf("migration published a new backend: %s %v", service, err)
				}
				if err := migrated.migrateSchema(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v101-before-v%d-*.db", centerSchemaVersion)))
			if err != nil || len(backups) != 1 {
				t.Fatalf("backup missing: %v %v", backups, err)
			}
			path := backups[0]
			if busy {
				path = filepath.Join(directory, "center.db")
			}
			original, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			var version, port int
			if err := original.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 101 {
				t.Fatalf("old schema lost: %d %v", version, err)
			}
			if err := original.QueryRow(`SELECT listen_port FROM meridian_endpoints WHERE id='host-endpoint'`).Scan(&port); err != nil || port != 443 {
				t.Fatalf("old endpoint lost: %d %v", port, err)
			}
		})
	}
}
