package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestExecutionFenceAllowsHeartbeatWithoutAutomaticRuntimeReplay(t *testing.T) {
	for _, state := range []string{"none", "running", "failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "heartbeat-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.22", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.22", LANAddress: "10.0.0.22", EnabledKinds: []string{networking.KindLAN}})
			app := installCPA(t, store, node, "10.0.0.22")
			if _, err := store.db.Exec(`UPDATE applications SET runtime_generation=0 WHERE id=?`, app); err != nil {
				t.Fatal(err)
			}
			if state != "none" {
				session := "heartbeat-fence-current-execution-session"
				if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
				auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: "interrupted-intent", Kind: "application.command", Attempt: 1})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
					t.Fatal(err)
				}
				if state != "running" {
					if err := store.StopExecution(ctx, node.ID, session, auth.ID, state == "unknown", "interrupted operation"); err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 3 {
				if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "observed-current", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration}); err != nil {
					t.Fatal(err)
				}
			}
			var deployments int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE agent_id=?`, node.ID).Scan(&deployments); err != nil {
				t.Fatal(err)
			}
			want := 1
			if state == "none" {
				want = 2
			}
			if deployments != want {
				t.Fatalf("heartbeat generated work despite %s fence: %d want %d", state, deployments, want)
			}
			var version string
			if err := store.db.QueryRow(`SELECT version FROM agents WHERE id=?`, node.ID).Scan(&version); err != nil || version != "observed-current" {
				t.Fatalf("fence stopped heartbeat observation: %s %v", version, err)
			}
		})
	}
}
