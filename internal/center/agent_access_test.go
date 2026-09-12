package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func enrollAccessTestNode(t *testing.T, store *Store, name, address string) AgentCredential {
	t.Helper()
	return enrollOrchestrationNode(t, store, name, NodeCapabilities{Docker: true, Gateway: true},
		[]networking.Candidate{{Address: address, Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: address, LANAddress: address, EnabledKinds: []string{networking.KindLAN}})
}

func TestStopOfflineAgentAccessPreservesApplicationsAndGateway(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now()
	store.now = func() time.Time { return clock }
	node := enrollAccessTestNode(t, store, "offline-node", "10.0.0.80")
	other := enrollAccessTestNode(t, store, "other-node", "10.0.0.81")
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at)
		SELECT 'preserved-app','Preserved app',id,site_id,'test/preserved','running','','' FROM agents WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if err := store.DisableAgent(ctx, node.ID); err == nil {
		t.Fatal("test node did not retain its active dependencies")
	}
	if err := store.RevokeAgentCredential(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test"}); err == nil {
		t.Fatal("stopped Agent heartbeat was accepted")
	}
	if _, err := store.WaitAndClaimNextTask(ctx, node.ID, node.Credential, 0); err == nil {
		t.Fatal("stopped Agent could claim a pending task")
	}
	if err := store.authenticateAgent(ctx, other.ID, other.Credential); err != nil {
		t.Fatalf("other node access changed: %v", err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, agent := range agents {
		if agent.ID == node.ID {
			found = true
			if agent.Status != "active" || agent.Connected || !agent.CredentialRevoked {
				t.Fatalf("access-stop status: status=%s connected=%t revoked=%t", agent.Status, agent.Connected, agent.CredentialRevoked)
			}
		}
	}
	if !found {
		t.Fatal("access stop deleted the node record")
	}
	var applicationStatus, deploymentState string
	var gateways, uninstalls int
	if err := store.db.QueryRow(`SELECT status FROM applications WHERE id='preserved-app'`).Scan(&applicationStatus); err != nil || applicationStatus != "running" {
		t.Fatalf("application changed: %s, %v", applicationStatus, err)
	}
	if err := store.db.QueryRow(`SELECT state FROM deployments WHERE id=?`, deployment.ID).Scan(&deploymentState); err != nil || deploymentState != "pending" {
		t.Fatalf("pending deployment changed: %s, %v", deploymentState, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_gateways WHERE agent_id=?`, node.ID).Scan(&gateways); err != nil || gateways != 1 {
		t.Fatalf("gateway association changed: %d, %v", gateways, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE agent_id=? AND operation='uninstall'`, node.ID).Scan(&uninstalls); err != nil || uninstalls != 0 {
		t.Fatalf("access stop queued an uninstall: %d, %v", uninstalls, err)
	}
}

func TestStopAgentAccessInvalidatesReconnectGrantsAndAllowsExplicitNewGrant(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now()
	store.now = func() time.Time { return clock }
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?), (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	node := enrollAccessTestNode(t, store, "offline-node", "10.0.0.80")
	other := enrollAccessTestNode(t, store, "other-node", "10.0.0.81")
	clock = clock.Add(time.Minute)
	stale, err := store.CreateAgentReconnectEnrollment(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := store.CreateAgentReconnectEnrollment(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Generating a reconnect command already revokes the old credential. An
	// explicit access stop must still succeed and invalidate that pending grant.
	for i := 0; i < 2; i++ {
		if err := store.RevokeAgentCredential(ctx, node.ID); err != nil {
			t.Fatalf("repeated access stop failed: %v", err)
		}
	}
	if _, err := store.AgentEnrollmentInstallProfile(ctx, stale.Token); err == nil {
		t.Fatal("stale reconnect install profile remained available")
	}
	if _, err := store.EnrollAgent(ctx, stale.Token, "test", "linux", "amd64", testAgentPublicKey(t)); err == nil {
		t.Fatal("old reconnect command restored access")
	}
	if _, err := store.AgentEnrollmentInstallProfile(ctx, unrelated.Token); err != nil {
		t.Fatalf("unrelated reconnect grant changed: %v", err)
	}
	current, err := store.CreateAgentReconnectEnrollment(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := store.EnrollAgent(ctx, current.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil || replacement.ID != node.ID {
		t.Fatalf("new explicit reconnect did not reuse the node: %v", err)
	}
	if err := store.authenticateAgent(ctx, replacement.ID, replacement.Credential); err != nil {
		t.Fatalf("new credential rejected: %v", err)
	}
	if err := store.authenticateAgent(ctx, node.ID, node.Credential); err == nil {
		t.Fatal("old credential was revived")
	}
}

func TestStopAgentAccessRollsBackIfGrantRevocationFails(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, store, "rollback-node", "10.0.0.80")
	if _, err := store.db.Exec(`CREATE TRIGGER reject_access_revoke BEFORE DELETE ON agent_enrollment_operations
		BEGIN SELECT RAISE(ABORT, 'test refusal'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAgentCredential(ctx, node.ID); err == nil {
		t.Fatal("partial revocation was accepted")
	}
	if err := store.authenticateAgent(ctx, node.ID, node.Credential); err != nil {
		t.Fatalf("failed revocation was not rolled back: %v", err)
	}
}
