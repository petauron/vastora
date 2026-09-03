package center

import (
	"context"
	"strings"
	"testing"

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

func heartbeatAgentUpdateVersion(t *testing.T, store *Store, node AgentCredential, version string, supported bool) {
	t.Helper()
	if err := store.RecordAgentHeartbeat(context.Background(), node.ID, node.Credential, NodeHeartbeat{
		Version: version, Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, RemoteUpdateSupported: supported,
	}); err != nil {
		t.Fatal(err)
	}
}
