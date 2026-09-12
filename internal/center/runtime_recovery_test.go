package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestRecoveryClaimSkipsUnrelatedDeploymentAndChecksOwner(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "repair", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	for index, fixture := range []struct{ id, key string }{{"unrelated", "vastora-official/keeper"}, {"broken", "vastora-official/cpa"}} {
		now := time.Now().UTC().Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) VALUES(?,?,?,?,?,'running',?,?)`, fixture.id, fixture.id, node.ID, testSiteID(t, store), fixture.key, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id) VALUES(?,?,?,'1',?,?,'uninstall','pending',?,?,?)`, "task-"+fixture.id, node.ID, fixture.key, []byte(`{}`), []byte(`{}`), now, now, fixture.id); err != nil {
			t.Fatal(err)
		}
	}
	scope := &controlplane.RecoveryScope{Stage: "application", Applications: []controlplane.RecoveryApplication{{AppKey: "vastora-official/cpa", ApplicationID: "wrong-owner", Reason: "state_incomplete"}}}
	if task, err := store.claimNextTask(ctx, node.ID, node.Credential, "", scope); err != nil || task != nil {
		t.Fatalf("foreign identity claimed: %#v %v", task, err)
	}
	scope.Applications[0].ApplicationID = "broken"
	// Even a correctly scoped repair must not consume a lease or starve the
	// next supported repair when it requires a newer Agent runtime generation.
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET runtime_generation=? WHERE id='task-unrelated'`, platform.ApplicationRuntimeGeneration+1); err != nil {
		t.Fatal(err)
	}
	scope.Applications = append(scope.Applications, controlplane.RecoveryApplication{AppKey: "vastora-official/keeper", ApplicationID: "unrelated", Reason: "image_unavailable"})
	task, err := store.claimNextTask(ctx, node.ID, node.Credential, "", scope)
	if err != nil || task == nil || task.ID != "task-broken" || task.Operation != "uninstall" {
		t.Fatalf("repair task=%#v err=%v", task, err)
	}
	var state string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM deployments WHERE id='task-unrelated'`).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("unrelated work consumed: state=%s err=%v", state, err)
	}
	if task, err := store.claimNextTask(ctx, node.ID, node.Credential, "task-unrelated", scope); err == nil || task != nil {
		t.Fatal("exact task claim accepted a competing repair scope")
	}
}

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
