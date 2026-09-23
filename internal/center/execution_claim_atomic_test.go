package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestExecutionClaimAuthorizationFailureRollsBackSelection(t *testing.T) {
	for _, kind := range []string{"application.apply", "agent.update", "gateway.component.apply"} {
		for _, failure := range []string{"insert", "commit"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				store := openOrchestrationStore(t)
				defer store.Close()
				ctx := context.Background()
				node := enrollOrchestrationNode(t, store, "atomic-claim", NodeCapabilities{Docker: true, Gateway: kind == "gateway.component.apply"}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
				version := Version
				roles := []string{"worker"}
				if kind == "gateway.component.apply" {
					roles = append(roles, "gateway")
				}
				if kind == "agent.update" {
					version = "0.1.0-alpha.123"
				}
				if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
					Version: version, Roles: roles, Capabilities: NodeCapabilities{Docker: true, Gateway: kind == "gateway.component.apply"},
					ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, RemoteUpdateSupported: true,
				}); err != nil {
					t.Fatal(err)
				}
				query := `SELECT status,attempt FROM gateway_components WHERE gateway_node_id=?`
				identity := node.ID
				if kind == "application.apply" {
					d, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
					if err != nil {
						t.Fatal(err)
					}
					identity = d.ID
					query = `SELECT state,attempt FROM deployments WHERE id=?`
				} else if kind == "agent.update" {
					u, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124")
					if err != nil {
						t.Fatal(err)
					}
					identity = u.ID
					query = `SELECT state,attempt FROM agent_updates WHERE id=?`
				}
				session := "atomic-claim-current-process-session"
				if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
				var original string
				var originalAttempt int64
				if err := store.db.QueryRow(query, identity).Scan(&original, &originalAttempt); err != nil {
					t.Fatal(err)
				}
				if failure == "insert" {
					if _, err := store.db.Exec(`CREATE TRIGGER fail_grant BEFORE INSERT ON task_executions BEGIN SELECT RAISE(ABORT,'simulated grant write failure'); END`); err != nil {
						t.Fatal(err)
					}
				} else {
					for _, sql := range []string{
						`CREATE TABLE claim_test_parent(id INTEGER PRIMARY KEY)`,
						`CREATE TABLE claim_test_child(parent INTEGER REFERENCES claim_test_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
						`CREATE TRIGGER fail_grant AFTER INSERT ON task_executions BEGIN INSERT INTO claim_test_child(parent) VALUES(1); END`,
					} {
						if _, err := store.db.Exec(sql); err != nil {
							t.Fatal(err)
						}
					}
				}
				if task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); err == nil || task != nil {
					t.Fatalf("failed persistence returned task: %+v %v", task, err)
				}
				var state string
				var attempt int64
				if err := store.db.QueryRow(query, identity).Scan(&state, &attempt); err != nil || state != original || attempt != originalAttempt {
					t.Fatalf("failed grant consumed intent: %s/%d want %s/%d err=%v", state, attempt, original, originalAttempt, err)
				}
				var executions, claims int
				if err := store.db.QueryRow(`SELECT count(*) FROM task_executions WHERE agent_id=?`, node.ID).Scan(&executions); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRow(`SELECT count(*) FROM task_events WHERE agent_id=? AND event='claimed'`, node.ID).Scan(&claims); err != nil {
					t.Fatal(err)
				}
				if executions != 0 || claims != 0 {
					t.Fatalf("partial authorization or audit survived: executions=%d claims=%d", executions, claims)
				}
				if _, err := store.db.Exec(`DROP TRIGGER fail_grant`); err != nil {
					t.Fatal(err)
				}
				type result struct {
					task *AgentTask
					err  error
				}
				results := make(chan result, 2)
				start := make(chan struct{})
				for range 2 {
					go func() {
						<-start
						task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
						results <- result{task, err}
					}()
				}
				close(start)
				winners := 0
				for range 2 {
					result := <-results
					if result.task == nil {
						if result.err == nil {
							t.Fatal("competing claim did not report its execution fence")
						}
						continue
					}
					winners++
					if result.err != nil || result.task.Kind != kind || result.task.Attempt != originalAttempt+1 || result.task.Authorization.ID == "" {
						t.Fatalf("intact task not authorized exactly once: %+v %v", result.task, result.err)
					}
				}
				if winners != 1 {
					t.Fatalf("concurrent claims produced %d authorizations", winners)
				}
				if err := store.db.QueryRow(`SELECT count(*) FROM task_executions WHERE agent_id=?`, node.ID).Scan(&executions); err != nil || executions != 1 {
					t.Fatalf("concurrent ledger entries=%d err=%v", executions, err)
				}
			})
		}
	}
}
