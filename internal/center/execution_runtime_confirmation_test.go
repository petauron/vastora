package center

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestExecutionRuntimeConfirmationPreservesEvidenceAndRevisionFence(t *testing.T) {
	for _, kind := range []string{"landing.server.apply", "landing.proxy.apply", "gateway.routes.apply", "gateway.component.apply", "node.listener.apply", "tunnel.state.apply"} {
		for _, mode := range []string{"success", "commit-failure", "newer-revision", "already-applied"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				store := openOrchestrationStore(t)
				defer store.Close()
				ctx := context.Background()
				node := enrollOrchestrationNode(t, store, "runtime-confirmation", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
				stamp := store.now().UTC().Format(time.RFC3339Nano)
				desired, _ := json.Marshal(map[string]any{"nodeId": node.ID, "revision": 7})
				var query, table, key, revisionColumn, appliedColumn, taskID string
				var args []any
				switch kind {
				case "landing.server.apply":
					table, key, revisionColumn, appliedColumn, taskID = "landing_server_states", "node_id", "desired_revision", "applied_revision", landingServerTaskID(node.ID, 7)
					query = `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,7,6,?,'failed',3,?)`
					args = []any{node.ID, desired, stamp}
				case "landing.proxy.apply":
					deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
					if err != nil {
						t.Fatal(err)
					}
					table, key, revisionColumn, appliedColumn, taskID = "landing_proxy_states", "node_id", "desired_revision", "applied_revision", landingProxyTaskID(node.ID, 7)
					query = `INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,?,?,1,'100.64.0.8',7,6,?,'failed',3,?)`
					args = []any{node.ID, deployment.ApplicationID, node.ID, desired, stamp}
				case "gateway.routes.apply":
					table, key, revisionColumn, appliedColumn, taskID = "gateway_states", "gateway_node_id", "desired_revision", "applied_revision", gatewayRouteTaskID(node.ID, 7)
					query = `INSERT INTO gateway_states(gateway_node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,7,6,?,'failed',3,?)`
					args = []any{node.ID, desired, stamp}
				case "gateway.component.apply":
					table, key, revisionColumn, appliedColumn, taskID = "gateway_components", "gateway_node_id", "generation", "applied_generation", gatewayComponentTaskID(node.ID, 7)
					query = `INSERT INTO gateway_components(gateway_node_id,desired_status,generation,applied_generation,status,attempt,updated_at) VALUES(?,'stopped',7,6,'failed',3,?)`
					args = []any{node.ID, stamp}
				case "node.listener.apply":
					table, key, revisionColumn, appliedColumn, taskID = "node_listener_states", "node_id", "desired_revision", "applied_revision", nodeListenerTaskID(node.ID, 7)
					query = `INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,7,6,?,'failed',3,?)`
					args = []any{node.ID, desired, stamp}
				case "tunnel.state.apply":
					if _, err := store.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('runtime-confirmation-token',X'00',?,?)`, stamp, stamp); err != nil {
						t.Fatal(err)
					}
					table, key, revisionColumn, appliedColumn, taskID = "cloudflare_tunnels", "agent_id", "desired_revision", "applied_revision", tunnelTaskID(node.ID, 7)
					query = `INSERT INTO cloudflare_tunnels(agent_id,tunnel_id,tunnel_name,token_secret_id,desired_revision,applied_revision,status,attempt,created_at,updated_at) VALUES(?,'confirmation-tunnel','confirmation-tunnel','runtime-confirmation-token',7,6,'failed',3,?,?)`
					args = []any{node.ID, stamp, stamp}
				}
				if _, err := store.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				session := "runtime-confirmation-original-session"
				if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
				auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: taskID, Kind: kind, Attempt: 3, Revision: 7})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "tunnel.state.apply" {
					// This test exercises retained-result projection, not the external
					// Cloudflare preflight owned by StartExecution.
					if _, err := store.db.Exec(`UPDATE task_executions SET state='running',phase='started' WHERE id=?`, auth.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
					t.Fatal(err)
				}
				if err := store.StoreExecutionResult(ctx, node.ID, session, auth.ID, json.RawMessage(`{}`), true, false, "", nil); err != nil {
					t.Fatal(err)
				}
				// Persisted evidence becomes manually confirmable only after the
				// original execution is fenced as unknown. Registering a replacement
				// session would now trigger automatic projection of valid evidence.
				if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown' WHERE id=?`, auth.ID); err != nil {
					t.Fatal(err)
				}
				cookie, _, err := store.CreateFirstAdmin(ctx, "confirmation-admin", "test-only-strong-password")
				if err != nil {
					t.Fatal(err)
				}
				adminID, err := store.SessionAdminID(ctx, cookie)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "newer-revision" {
					if _, err := store.db.Exec("UPDATE "+table+" SET "+revisionColumn+"=8 WHERE "+key+"=?", node.ID); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "already-applied" {
					if _, err := store.db.Exec("UPDATE "+table+" SET "+appliedColumn+"=7 WHERE "+key+"=?", node.ID); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "commit-failure" {
					for _, statement := range []string{`CREATE TABLE confirmation_parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE confirmation_child(parent INTEGER REFERENCES confirmation_parent(id) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER reject_confirmation AFTER UPDATE ON task_executions WHEN NEW.disposition='confirm-completed' BEGIN INSERT INTO confirmation_child(parent) VALUES(1); END`} {
						if _, err := store.db.Exec(statement); err != nil {
							t.Fatal(err)
						}
					}
				}
				var before []byte
				if err := store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, auth.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				decision := controlplane.ExecutionDisposition{Action: "confirm-completed", ExecutionStopped: true, Note: "Verified stopped executor and actual resource state."}
				err = store.ConfirmExecution(ctx, auth.ID, adminID, decision)
				if (err == nil) != (mode == "success") {
					t.Fatalf("confirmation: %v", err)
				}
				var state, disposition, business string
				var after []byte
				var applied, attempt int
				if err := store.db.QueryRow(`SELECT state,disposition,sealed_result FROM task_executions WHERE id=?`, auth.ID).Scan(&state, &disposition, &after); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRow("SELECT status,"+appliedColumn+",attempt FROM "+table+" WHERE "+key+"=?", node.ID).Scan(&business, &applied, &attempt); err != nil {
					t.Fatal(err)
				}
				if state != "unknown" || !bytes.Equal(before, after) || attempt != 3 {
					t.Fatal("historical execution or evidence changed")
				}
				if mode == "success" {
					if disposition != "confirm-completed" || applied != 7 || (business != "ready" && business != "stopped") {
						t.Fatal("confirmation lacks matching business outcome")
					}
					if err := store.ConfirmExecution(ctx, auth.ID, adminID, decision); err == nil {
						t.Fatal("confirmation replayed")
					}
				} else {
					wantApplied := 6
					if mode == "already-applied" {
						wantApplied = 7
					}
					if disposition != "" || business != "failed" || applied != wantApplied {
						t.Fatal("rejected confirmation left partial business changes")
					}
				}
			})
		}
	}
}

func TestExecutionProjectionAcceptsAppliedLandingRevisionWithQueuedSuccessor(t *testing.T) {
	for _, kind := range []string{"landing.server.apply", "landing.proxy.apply"} {
		t.Run(kind, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "landing-successor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.21", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.21", LANAddress: "10.0.0.21", EnabledKinds: []string{networking.KindLAN}})
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			taskID := landingServerTaskID(node.ID, 8)
			if kind == "landing.server.apply" {
				if _, err := store.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,9,8,'{}','pending',7,?)`, node.ID, stamp); err != nil {
					t.Fatal(err)
				}
			} else {
				deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
				if err != nil {
					t.Fatal(err)
				}
				taskID = landingProxyTaskID(node.ID, 8)
				if _, err := store.db.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,?,?,1,'100.64.0.21',9,8,'{}','pending',7,?)`, node.ID, deployment.ApplicationID, node.ID, stamp); err != nil {
					t.Fatal(err)
				}
			}
			const executionID = "landing-successor-execution"
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES(?,?,?,?,7,'landing-successor-session','landing-successor-digest',X'00','unknown','result_received',?,?,?)`, executionID, node.ID, taskID, kind, stamp, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := validateExecutionProjection(ctx, tx, executionID, true); err != nil {
				t.Fatalf("applied revision was rejected after its successor was queued: %v", err)
			}
		})
	}
}
