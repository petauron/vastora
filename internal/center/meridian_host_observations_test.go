package center

import (
	"context"
	"testing"
)

func TestMeridianHostObservationCannotRewriteBackend(t *testing.T) {
	for _, mode := range []string{"legacy-heartbeat", "pending", "network-drift", "hy2-only"} {
		t.Run(mode, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			ctx := context.Background()
			observation := ApplicationEndpointObservation{AppKey: meridianAppKey, Name: "inbound-1", Protocol: "tcp", AppProtocol: meridianEntryProtocol, Listen: "100.64.0.61", Port: 10443, Enabled: true, InboundTag: "shared-entry"}
			var nodeID string
			if err := store.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "legacy-heartbeat":
				observation.Listen = "0.0.0.0"
				observation.Port = 443
			case "pending":
				if _, err := store.db.Exec(`UPDATE meridian_endpoints SET desired_revision=desired_revision+1,status='pending'`); err != nil {
					t.Fatal(err)
				}
				observation.Listen = "0.0.0.0"
				observation.Port = 443
			case "network-drift":
				if _, err := store.db.Exec(`UPDATE agent_network_profiles SET service_address='100.64.0.62' WHERE agent_id=?`, nodeID); err != nil {
					t.Fatal(err)
				}
			case "hy2-only":
				if _, err := store.db.Exec(`UPDATE meridian_endpoints SET vless_enabled=0,hy2_enabled=1,hy2_inbound_tag='hy2-entry',hy2_server_name='entry.example.test',(hy2_certificate_secret_id,hy2_private_key_secret_id)=(private_key_secret_id,private_key_secret_id)`); err != nil {
					t.Fatal(err)
				}
				observation.InboundTag = "hy2-entry"
				observation.Listen = "0.0.0.0"
				observation.Port = 443
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var cleanups []publicationCleanup
			if err := store.reconcileMeridianEndpoints(ctx, tx, nodeID, []ApplicationEndpointObservation{observation}, store.now(), &cleanups); err != nil {
				t.Fatal(err)
			}
			var endpoint, status string
			var port int
			if err := tx.QueryRow(`SELECT endpoint,container_port FROM services WHERE id='snapshot-shared-service'`).Scan(&endpoint, &port); err != nil {
				t.Fatal(err)
			}
			if endpoint != "100.64.0.61:10443" || port != 10443 {
				t.Fatalf("backend drifted: %s/%d", endpoint, port)
			}
			if err := tx.QueryRow(`SELECT status FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if mode == "pending" && status != "pending" || mode == "legacy-heartbeat" && status != "failed" || mode == "network-drift" && status != "failed" {
				t.Fatalf("unexpected status %s", status)
			}
			if mode == "hy2-only" {
				state, err := store.desiredNodeListenerState(ctx, tx, nodeID, 2)
				if err != nil || len(state.Listener.Routes) != 0 {
					t.Fatalf("HY2 generated a TCP route: %+v %v", state, err)
				}
			}
		})
	}
}
