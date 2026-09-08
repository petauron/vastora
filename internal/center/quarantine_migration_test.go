package center

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestMigrationPreservesExplicitlyStoppedEntriesAcrossVersion56(t *testing.T) {
	for _, action := range []int{0, 1} {
		t.Run(fmt.Sprintf("action_required_%d", action), func(t *testing.T) {
			testMigrationPreservesStoppedEntry(t, action)
		})
	}
}

func testMigrationPreservesStoppedEntry(t *testing.T, originalAction int) {
	directory := t.TempDir()
	store := legacyMigrationStore(t, directory, 55)
	if _, err := store.db.Exec(`UPDATE publications SET kind='cloudflare_tunnel', dns_provider='cloudflare', status='stopped', last_error='retained stop reason' WHERE id='publication-v3'`); err != nil {
		t.Fatal(err)
	}
	// Version 55 has no action_required column. Migration 56 derives it from
	// the old topology; exercise an actual legacy condition, not a new field.
	if originalAction == 1 {
		if _, err := store.db.Exec(`UPDATE publications SET gateway_node_id=NULL WHERE id='publication-v3'`); err != nil {
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
	var state, reason string
	var action, routes, staging int
	if err := migrated.db.QueryRow(`SELECT status,last_error,action_required FROM publications WHERE id='publication-v3'`).Scan(&state, &reason, &action); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM routes WHERE publication_id='publication-v3'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='migration_56_stopped_publications'`).Scan(&staging); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || reason != "retained stop reason" || action != originalAction || routes != 0 || staging != 0 {
		t.Fatalf("explicit stop changed: state=%s reason=%s action=%d routes=%d staging=%d", state, reason, action, routes, staging)
	}
}

func TestVersion64MigrationWithdrawsOnlyUnsafeRealitySnapshots(t *testing.T) {
	store := legacyMigrationStore(t, t.TempDir(), 63)
	defer store.Close()
	ctx := context.Background()
	for _, statement := range []string{
		`UPDATE applications SET app_key = 'vastora-official/3x-ui', role = 'master' WHERE id = 'application-v3'`,
		`UPDATE services SET app_protocol = 'vless/tcp/reality', protocol = 'tcp' WHERE id = 'service-v3'`,
		`UPDATE publications SET status = 'pending' WHERE id = 'publication-v3'`,
		`INSERT INTO three_x_ui_reality_guards(service_id,target_host,target_ip,server_name,companion_tag,status,created_at,updated_at)
		 VALUES('service-v3','www.example.com','1.1.1.1','www.example.com','','action_required','2026-09-08','2026-09-08')`,
		`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at)
		 VALUES('healthy-service','application-v3','site-v3','inbound-2','tcp',443,443,'10.0.0.2:443','observed','vless/tcp/reality','ready','2026-09-08','2026-09-08')`,
		`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,status,created_at,updated_at)
		 VALUES('healthy-publication','healthy-service','public_shared_443','application_node','agent-v3','healthy.example.test','manual','ready','2026-09-08','2026-09-08')`,
		`INSERT INTO three_x_ui_reality_guards(service_id,target_host,target_ip,server_name,companion_tag,status,created_at,updated_at)
		 VALUES('healthy-service','www.example.com','1.1.1.1','www.example.com','','ready','2026-09-08','2026-09-08')`,
		`INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,lease_expires_at,updated_at)
		 VALUES('agent-v3',7,7,'{"revision":7,"nodeId":"agent-v3","listener":{"routes":[{"id":"publication-v3"},{"id":"healthy-publication"}]}}','applying','2099-01-01','2026-09-08')`,
		`INSERT INTO gateway_states(gateway_node_id,desired_revision,applied_revision,desired_json,status,lease_expires_at,updated_at)
		 VALUES('agent-v3',9,9,'{"revision":9,"sharedHttps":{"routes":[{"id":"route-v3"},{"id":"healthy-publication"}]}}','applying','2099-01-01','2026-09-08')`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	provider, err := newMigrationProvider(store.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 64); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"node_listener_states", "gateway_states"} {
		var desired, applied int64
		var state, lease string
		var raw []byte
		if err := store.db.QueryRow(`SELECT desired_revision,applied_revision,desired_json,status,lease_expires_at FROM `+table).Scan(&desired, &applied, &raw, &state, &lease); err != nil {
			t.Fatal(err)
		}
		var value struct {
			Revision    int64                                  `json:"revision"`
			Listener    struct{ Routes []struct{ ID string } } `json:"listener"`
			SharedHTTPS struct{ Routes []struct{ ID string } } `json:"sharedHttps"`
		}
		if json.Unmarshal(raw, &value) != nil || desired != applied+1 || value.Revision != desired || state != "pending" || lease != "" {
			t.Fatalf("stale listener task was not invalidated: %s", raw)
		}
		routes := value.Listener.Routes
		if table == "gateway_states" {
			routes = value.SharedHTTPS.Routes
		}
		if len(routes) != 1 || routes[0].ID != "healthy-publication" {
			t.Fatalf("wrong listener routes retained: %s", raw)
		}
	}
	var unsafe, healthy string
	var action, revision, routes int
	if err := store.db.QueryRow(`SELECT status,action_required FROM publications WHERE id='publication-v3'`).Scan(&unsafe, &action); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status,desired_revision FROM publications WHERE id='healthy-publication'`).Scan(&healthy, &revision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM routes WHERE publication_id='publication-v3'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if unsafe != "stopped" || action != 1 || healthy != "ready" || revision != 1 || routes != 0 {
		t.Fatalf("unsafe=%s action=%d healthy=%s revision=%d routes=%d", unsafe, action, healthy, revision, routes)
	}
}
