package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestRuntimeMigrationPreservesInterruptedRuntimeRevisions(t *testing.T) {
	for _, state := range []string{"pending", "applying", "failed"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "runtime-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('test-tunnel-secret',X'00',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO cloudflare_tunnels(agent_id,tunnel_id,tunnel_name,token_secret_id,desired_revision,applied_revision,status,attempt,created_at,updated_at) VALUES(?,'test-tunnel','test-tunnel','test-tunnel-secret',7,6,?,3,?,?)`, node.ID, state, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			queries := []string{
				`INSERT INTO gateway_components(gateway_node_id,desired_status,generation,applied_generation,status,attempt,updated_at) VALUES(?,'running',7,6,?,3,?)`,
				`INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,7,6,'{}',?,3,?)`,
			}
			for _, query := range queries {
				if _, err := store.db.Exec(query, node.ID, state, stamp); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately invalid desired_json must never be interpreted when the
			// current runtime attempt is not complete and ready.
			for i := 0; i < 3; i++ {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.queueApplicationRuntimeMigration(ctx, tx, node.ID, 100+i, time.Now().UTC()); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			for _, query := range []string{
				`SELECT generation,attempt,status FROM gateway_components WHERE gateway_node_id=?`,
				`SELECT desired_revision,attempt,status FROM node_listener_states WHERE node_id=?`,
				`SELECT desired_revision,attempt,status FROM cloudflare_tunnels WHERE agent_id=?`,
			} {
				var revision, attempt int
				var actual string
				if err := store.db.QueryRow(query, node.ID).Scan(&revision, &attempt, &actual); err != nil || revision != 7 || attempt != 3 || actual != state {
					t.Fatalf("runtime intent changed: revision=%d attempt=%d state=%s err=%v", revision, attempt, actual, err)
				}
			}
		})
	}
}
