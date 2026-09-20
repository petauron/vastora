package center

import "testing"

func TestAgentUpdateRolloutAvailabilityOnlyRequiresStartupAndBinaries(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	server := NewServer(store, "", false)
	server.startupReady.Store(true)
	server.agentBinariesDir = "unused-test-binaries"
	if !server.agentUpdateRolloutAvailable() {
		t.Fatal("running Center with packaged binaries did not enable Agent rollout")
	}

	server.startupReady.Store(false)
	if server.agentUpdateRolloutAvailable() {
		t.Fatal("Center startup did not block Agent rollout")
	}
	server.startupReady.Store(true)
	server.agentBinariesDir = ""
	if server.agentUpdateRolloutAvailable() {
		t.Fatal("missing binary directory did not block Agent rollout")
	}
}

func TestAgentUpdateRolloutIgnoresUnrelatedReconciliationState(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	server := NewServer(store, "", false)
	server.startupReady.Store(true)
	server.agentBinariesDir = "unused-test-binaries"

	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, setupGatewayBindingSetting, "invalid"); err != nil {
		t.Fatal(err)
	}
	if !server.agentUpdateRolloutAvailable() {
		t.Fatal("unrelated gateway reconciliation blocked independent Agent updates")
	}
}
