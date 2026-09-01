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

func heartbeatAgentUpdateVersion(t *testing.T, store *Store, node AgentCredential, version string, supported bool) {
	t.Helper()
	if err := store.RecordAgentHeartbeat(context.Background(), node.ID, node.Credential, NodeHeartbeat{
		Version: version, Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, RemoteUpdateSupported: supported,
	}); err != nil {
		t.Fatal(err)
	}
}
