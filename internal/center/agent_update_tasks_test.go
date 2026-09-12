package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestAgentUpdateRequiresHandoffAndTargetVersionReconnect(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.Kind != "agent.update" || task.ID != queued.ID || task.TargetVersion != "0.1.0-alpha.89" {
		t.Fatalf("unexpected Agent update task: %#v", task)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("Agent update completed before durable helper handoff")
	}
	if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
		t.Fatalf("duplicate update handoff was not idempotent: %v", err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("Agent update completed before the target version reconnected")
	}
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err != nil {
		t.Fatal(err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Version != "0.1.0-alpha.89" || agents[0].Update == nil || agents[0].Update.State != "succeeded" {
		t.Fatalf("updated Agent state was not exposed: %#v", agents)
	}
}

func TestAgentUpdateRecoveryRemainsActiveUntilTargetReconnects(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "update-recovery-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.99", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.99", LANAddress: "10.0.0.99", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil {
		t.Fatalf("claim update: %#v, %v", task, err)
	}
	if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
		t.Fatal(err)
	}
	const recoveryRequired = "recovery required; schema-compatible candidate remains installed"
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err != nil {
			t.Fatalf("recovery report replay failed: %v", err)
		}
	}
	var state, lastError string
	if err := store.db.QueryRowContext(ctx, `SELECT state, last_error FROM agent_updates WHERE id = ?`, task.ID).Scan(&state, &lastError); err != nil || state != "installing" || lastError != recoveryRequired {
		t.Fatalf("recovery no longer owns the update: state=%q error=%q err=%v", state, lastError, err)
	}
	var failedActivations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event = 'failed'`, task.ID).Scan(&failedActivations); err != nil || failedActivations != 1 {
		t.Fatalf("replayed recovery reports duplicated activation events: count=%d err=%v", failedActivations, err)
	}
	if repeated, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89"); err != nil || repeated.ID != queued.ID {
		t.Fatalf("manual retry replaced the recovering attempt: %#v %v", repeated, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.90"); err == nil {
		t.Fatal("a competing version replaced an active recovery")
	}
	if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(nodes) != 0 {
		t.Fatalf("automatic rollout replaced an active recovery: %v %v", nodes, err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("recovery succeeded without a target-version heartbeat")
	}
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt+1, false, "stale", nil, true); err == nil {
		t.Fatal("a stale attempt changed recovery state")
	}
	if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
		t.Fatalf("recovering update could not resume the same durable attempt: %v", err)
	}
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err != nil {
		t.Fatalf("recovered target heartbeat could not complete update: %v", err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Update == nil || agents[0].Update.ID != queued.ID || agents[0].Update.State != "succeeded" || agents[0].Update.LastError != "" {
		t.Fatalf("recovered update state = %#v", agents)
	}
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err == nil {
		t.Fatal("a late recovery report reopened a completed update")
	}
}

func TestAgentUpdateRequiresAFeatureCapableOnlineAgent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "legacy-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", false)

	if _, err := store.QueueAgentUpdate(context.Background(), node.ID, "0.1.0-alpha.89"); err == nil || !strings.Contains(err.Error(), "one manual update") {
		t.Fatalf("legacy Agent update was not rejected with migration guidance: %v", err)
	}
}

func TestAgentUpdateRolloutQueuesOnlineAgentsConcurrently(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	first := enrollOrchestrationNode(t, store, "rollout-a", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
	second := enrollOrchestrationNode(t, store, "rollout-b", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.97", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.97", LANAddress: "10.0.0.97", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, first, "0.1.0-alpha.88", true)
	heartbeatAgentUpdateVersion(t, store, second, "0.1.0-alpha.88", true)

	queuedIDs, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89")
	if err != nil || len(queuedIDs) != 2 {
		t.Fatalf("rollout queue = %#v, %v", queuedIDs, err)
	}
	if queuedAgain, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queuedAgain) != 0 {
		t.Fatalf("duplicate rollout queue = %#v, %v", queuedAgain, err)
	}
	firstTask, err := store.ClaimNextTask(ctx, first.ID, first.Credential)
	if err != nil || firstTask == nil || firstTask.Kind != "agent.update" {
		t.Fatalf("claim first concurrent rollout task: %#v, %v", firstTask, err)
	}
	secondTask, err := store.ClaimNextTask(ctx, second.ID, second.Credential)
	if err != nil || secondTask == nil || secondTask.Kind != "agent.update" {
		t.Fatalf("claim second concurrent rollout task: %#v, %v", secondTask, err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 2 || status.Updated != 0 || status.Updating != 2 || status.Pending != 0 {
		t.Fatalf("unexpected rollout status: %#v", status)
	}
	clock = clock.Add(agentConnectedMaxAge)
	heartbeatAgentUpdateVersion(t, store, second, "0.1.0-alpha.88", true)
	status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || status.Total != 2 || status.Updating != 1 || status.Offline != 1 {
		t.Fatalf("offline Agent obscured the remaining online update: %#v, %v", status, err)
	}
}

func TestAgentUpdateRolloutLeavesFailedTargetsForManualRetry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "rollout-failure", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.98", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.98", LANAddress: "10.0.0.98", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 1 {
		t.Fatalf("queue rollout: %#v, %v", queued, err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil {
		t.Fatalf("claim rollout: %#v, %v", task, err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "host update failed", nil, 0); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 0 {
		t.Fatalf("failed rollout was automatically retried: %#v, %v", queued, err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if status.Failed != 1 || status.Pending != 0 || status.Updating != 0 {
		t.Fatalf("unexpected failed rollout status: %#v", status)
	}
}

func TestAgentUpdateRolloutSkipsOfflineAgentsUntilReconnect(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "offline-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	clock = clock.Add(agentConnectedMaxAge)

	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 0 {
		t.Fatalf("offline Agent was queued automatically: %v, %v", queued, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89"); err == nil || !strings.Contains(err.Error(), "must be online") {
		t.Fatalf("offline Agent accepted a manual update: %v", err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || status.Total != 1 || status.Offline != 1 || status.Updating != 0 || status.Pending != 0 {
		t.Fatalf("offline rollout status = %#v, %v", status, err)
	}

	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("reconnected Agent was not queued: %v, %v", queued, err)
	}
}

func TestAgentUpdateRolloutOfflineTasksRemainRecoverable(t *testing.T) {
	for _, state := range []string{"pending", "running", "installing"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			clock := store.now().UTC()
			store.now = func() time.Time { return clock }
			node := enrollOrchestrationNode(t, store, "interrupted-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
			queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
			if err != nil {
				t.Fatal(err)
			}
			var task *AgentTask
			if state != "pending" {
				task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
				if err != nil || task == nil || task.ID != queued.ID {
					t.Fatalf("claim update: %#v, %v", task, err)
				}
			}
			if state == "installing" {
				if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
					t.Fatal(err)
				}
			}
			status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updating != 1 || status.Offline != 0 {
				t.Fatalf("online update status = %#v, %v", status, err)
			}

			clock = clock.Add(agentConnectedMaxAge)
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Total != 1 || status.Offline != 1 || status.Updating != 0 || status.Updated != 0 || status.Failed != 0 || status.Pending != 0 {
				t.Fatalf("offline update kept rollout busy: %#v, %v", status, err)
			}
			if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(nodes) != 0 {
				t.Fatalf("offline update was queued again: %v, %v", nodes, err)
			}
			var persistedState string
			if err := store.db.QueryRowContext(ctx, `SELECT state FROM agent_updates WHERE id = ?`, queued.ID).Scan(&persistedState); err != nil || persistedState != state {
				t.Fatalf("offline status changed durable task: state=%q err=%v", persistedState, err)
			}

			clock = clock.Add(taskLeaseDuration)
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updating != 1 || status.Offline != 0 {
				t.Fatalf("reconnected update status = %#v, %v", status, err)
			}
			if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(nodes) != 0 {
				t.Fatalf("reconnect duplicated the durable update: %v, %v", nodes, err)
			}
			if state != "installing" {
				task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
				if err != nil || task == nil || task.ID != queued.ID {
					t.Fatalf("resume update: %#v, %v", task, err)
				}
				if state == "running" && task.Attempt != 2 {
					t.Fatalf("expired download attempt was not reclaimed: %#v", task)
				}
			}
			if err := store.beginAgentUpdate(ctx, node.ID, node.Credential, task.ID, task.Attempt); err != nil {
				t.Fatal(err)
			}
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
			if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err != nil {
				t.Fatal(err)
			}
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updated != 1 || status.Updating != 0 || status.Offline != 0 {
				t.Fatalf("reconnected update did not complete: %#v, %v", status, err)
			}
		})
	}
}

func heartbeatAgentUpdateVersion(t *testing.T, store *Store, node AgentCredential, version string, supported bool) {
	t.Helper()
	if err := store.RecordAgentHeartbeat(context.Background(), node.ID, node.Credential, NodeHeartbeat{
		Version: version, Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, RemoteUpdateSupported: supported,
	}); err != nil {
		t.Fatal(err)
	}
}
