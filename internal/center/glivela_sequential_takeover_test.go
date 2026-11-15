package center

import (
	"github.com/petauron/vastora/internal/networking"
	"testing"
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
