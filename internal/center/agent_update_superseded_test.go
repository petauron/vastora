package center

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestInstalledUpdateExecutionDoesNotBlockOrdinaryWork(t *testing.T) {
	for _, state := range []string{"unknown", "running"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "update-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.134", true)
			session := "replacement-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			now := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,attempt,last_error,created_at,updated_at) VALUES('installed-update',?,'0.1.0-alpha.134','failed',1,'preserved update evidence',?,?)`, node.ID, now, now); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES('installed-update-execution',?,'installed-update','agent.update',1,'old-session','digest',X'00',?,'reported','',?,?)`, node.ID, state, now, now); err != nil {
				t.Fatal(err)
			}
			deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
			if err != nil {
				t.Fatal(err)
			}
			claimErr := store.executionClaimAllowed(ctx, node.ID, session)
			if state == "running" {
				if !errors.Is(claimErr, errExecutionBlocked) {
					t.Fatalf("active update did not retain the session claim fence: %v", claimErr)
				}
			} else if claimErr != nil {
				t.Fatalf("installed update stranded the Agent task channel: %v", claimErr)
			}
			task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
			if state == "running" {
				if !errors.Is(err, errExecutionBlocked) || task != nil {
					t.Fatalf("active update did not retain the execution fence: %+v %v", task, err)
				}
				return
			}
			if err != nil || task == nil || task.ID != deployment.ID || task.Authorization.ID == "" {
				t.Fatalf("installed update stranded ordinary work: %+v %v", task, err)
			}
			var preservedState, disposition string
			if err := store.db.QueryRow(`SELECT state,disposition FROM task_executions WHERE id='installed-update-execution'`).Scan(&preservedState, &disposition); err != nil || preservedState != "unknown" || disposition != "" {
				t.Fatalf("old execution evidence was rewritten: %s %s %v", preservedState, disposition, err)
			}
		})
	}
}

func TestInstalledVersionSupersedesOldFailureWithoutRewritingEvidence(t *testing.T) {
	for _, failedTarget := range []string{"0.1.0-alpha.129", "0.1.0-alpha.134"} {
		for _, single := range []bool{false, true} {
			store := openOrchestrationStore(t)
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "manually-updated", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.134", true)
			now := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,last_error,created_at,updated_at) VALUES('old-failure',?,?,'failed','original failure',?,?)`, node.ID, failedTarget, now, now); err != nil {
				t.Fatal(err)
			}
			status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.135")
			if err != nil || status.Pending != 1 || status.Manual != 0 {
				t.Fatalf("stale failure blocks future rollout: %+v %v", status, err)
			}
			if single {
				if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.135"); err != nil {
					t.Fatal(err)
				}
			} else if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.135"); err != nil || len(queued) != 1 {
				t.Fatalf("rollout did not queue manually upgraded Agent: %v %v", queued, err)
			}
			var state, message string
			if err := store.db.QueryRow(`SELECT state,last_error FROM agent_updates WHERE id='old-failure'`).Scan(&state, &message); err != nil || state != "failed" || message != "original failure" {
				t.Fatalf("old evidence rewritten: %s %s %v", state, message, err)
			}
			store.Close()
		}
	}
}

func TestNewerVersionDoesNotBypassRuntimeRecovery(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "recovery-blocked", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.134", true)
	if _, err := store.db.Exec(`UPDATE agents SET runtime_recovery='operator verification required' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.135"); err != nil || len(queued) != 0 {
		t.Fatalf("automatic update bypassed recovery: %v %v", queued, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.135"); err == nil {
		t.Fatal("individual update bypassed recovery")
	}
	if status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.135"); err != nil || status.Blocked != 1 || status.Manual != 0 || status.Pending != 0 {
		t.Fatalf("recovery incorrectly displayed as pending: %+v %v", status, err)
	}
}
