package center

import (
	"context"
	"path/filepath"
	"testing"
)

func TestVersion100DestinationDomains(t *testing.T) {
	for _, state := range []string{"ready", "failed", "retired", "busy"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyMigrationStore(t, directory, 99)
			ctx := context.Background()
			status := state
			if state == "busy" {
				status = "ready"
			}
			const stamp = "2026-01-01T00:00:00Z"
			if _, err := legacy.db.ExecContext(ctx, `INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('domain-key',X'010203',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.db.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
				VALUES('domain-endpoint','application-v3','service-v3','domain-entry',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]','domain-key','test-public-key','["abcd"]',4,4,1,?,?,?)`, status, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if state == "busy" {
				if _, err := legacy.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
					VALUES('domain-command','application-v3','site-v3','Legacy App','agent-v3','agent-v3','meridian.runtime.apply','{"endpointId":"domain-endpoint"}','running',?,?)`, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacy.db.Close(); err != nil {
				t.Fatal(err)
			}
			migrated, err := Open(directory)
			if state == "busy" {
				if err == nil {
					_ = migrated.Close()
					t.Fatal("migration accepted an executing old runtime command")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = migrated.Close() })
			for attempt := 0; attempt < 2; attempt++ {
				var desired, applied, healthy int
				var actual string
				if err := migrated.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,runtime_healthy,status FROM meridian_endpoints WHERE id='domain-endpoint'`).Scan(&desired, &applied, &healthy, &actual); err != nil {
					t.Fatal(err)
				}
				if state == "ready" {
					if desired != 5 || applied != 4 || healthy != 0 || actual != "pending" {
						t.Fatalf("ready endpoint was not safely revised: %d %d %d %s", desired, applied, healthy, actual)
					}
				} else if desired != 4 || applied != 4 || actual != state {
					t.Fatal("migration changed failed or retired endpoint")
				}
				if err := migrated.migrateSchema(ctx); err != nil {
					t.Fatal(err)
				}
			}
			backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v99-before-v100-*.db"))
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing migration backup: %v %v", backups, err)
			}
		})
	}
}
