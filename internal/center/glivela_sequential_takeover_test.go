package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

// Keep the original entry in place while adding a second independently owned
// entry. No production identifiers or credentials belong in these fixtures.
func seedSecondTakeoverEntry(t *testing.T, store *Store) {
	t.Helper()
	node := enrollOrchestrationNode(t, store, "second-entry", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.62", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.62", HeadscaleAddress: "100.64.0.62", EnabledKinds: []string{networking.KindHeadscale}})
	_, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		SELECT 'second-app',name,?,site_id,app_key,image,status,runtime,role,created_at,updated_at FROM applications WHERE id='snapshot-shared-app';
		INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at)
		SELECT 'second-service','second-app',site_id,'second-entry',protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at FROM services WHERE id='snapshot-shared-service';
		INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,status,created_at,updated_at)
		SELECT 'second-endpoint','second-app','second-service','second-entry',listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,status,created_at,updated_at FROM meridian_endpoints WHERE id='snapshot-shared-endpoint'`, node.ID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGlivelaSequentialMigrationPreservesOriginalCheckpoint(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	seedSecondTakeoverEntry(t, s)
	// Recreate the released singleton shape, including its exact checkpoint
	// bytes. The forward migration must preserve that row, not re-activate it.
	_, err := s.db.Exec(`DROP TABLE glivela_pilot_checkpoint;
		CREATE TABLE glivela_pilot_checkpoint (
		 id INTEGER PRIMARY KEY CHECK(id=1),
		 endpoint_id TEXT NOT NULL UNIQUE REFERENCES meridian_endpoints(id),
		 checkpoint_json BLOB NOT NULL CHECK(json_valid(checkpoint_json)),updated_at TEXT NOT NULL);
		INSERT INTO glivela_pilot_checkpoint VALUES(1,'snapshot-shared-endpoint','{"retained":true}','original');
		DELETE FROM goose_db_version WHERE version_id>=118; PRAGMA user_version=117`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newMigrationProvider(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(context.Background(), 118); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES('second-endpoint','{}','new')`); err != nil {
		t.Fatal("second entry rejected", err)
	}
	var raw, stamp string
	if err := s.db.QueryRow(`SELECT checkpoint_json,updated_at FROM glivela_pilot_checkpoint WHERE id=1 AND endpoint_id='snapshot-shared-endpoint'`).Scan(&raw, &stamp); err != nil || raw != `{"retained":true}` || stamp != "original" {
		t.Fatal("original checkpoint changed", err)
	}
	if _, err := s.db.Exec(`INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES('second-endpoint','{}','duplicate')`); err == nil {
		t.Fatal("duplicate ownership accepted")
	}
}
