package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestRecoveryHeartbeatUsesBoundedReasonsAndClearsDetails(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "recovery-status", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	heartbeat := NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, RuntimeRecovery: "application", RuntimeRecoveryApplications: []controlplane.RecoveryApplication{{AppKey: "vastora-official/cpa", ApplicationID: "broken", Reason: "image_unavailable"}}}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 1 || len(agents[0].RuntimeRecoveryApplications) != 1 || agents[0].RuntimeRecoveryApplications[0].Reason != "image_unavailable" || agents[0].GatewayHealthy {
		t.Fatalf("recovery status=%#v err=%v", agents, err)
	}
	heartbeat.RuntimeRecoveryApplications[0].Reason = "private path or secret"
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err == nil {
		t.Fatal("unbounded runtime error accepted")
	}
	heartbeat.RuntimeRecoveryApplications = nil
	heartbeat.RuntimeRecovery = "landing"
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatalf("landing recovery heartbeat rejected: %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key=?`, runtimeRecoverySettingsPrefix+node.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale recovery detail retained: %d %v", count, err)
	}
	agents, err = store.ListAgents(ctx)
	encoded, _ := json.Marshal(agents)
	if err != nil || strings.Contains(string(encoded), "image_unavailable") || agents[0].RuntimeRecovery != "landing" {
		t.Fatalf("stale public recovery status=%s err=%v", encoded, err)
	}
}
