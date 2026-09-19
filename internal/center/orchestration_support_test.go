package center

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func installCPA(t *testing.T, store *Store, node AgentCredential, address string) string {
	t.Helper()
	deployment, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", cpaApplicationResult(address))
	return deployment.ApplicationID
}

func cpaApplicationResult(address string) json.RawMessage {
	return json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"` + address + `"},{"name":"client-api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"` + address + `"}]}`)
}

func claimTask(t *testing.T, store *Store, node AgentCredential) *AgentTask {
	t.Helper()
	task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("expected an Agent task")
	}
	return task
}

func completeNextTask(t *testing.T, store *Store, node AgentCredential, kind string, result json.RawMessage) {
	t.Helper()
	task := claimTask(t, store, node)
	if task.Kind != kind {
		t.Fatalf("got task kind %q, want %q", task.Kind, kind)
	}
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
}

func openOrchestrationStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func selectTestThreeXUIController(t *testing.T, store *Store, applicationID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO three_x_ui_control_plane(id, controller_application_id, selection_reason, selected_at)
		VALUES(1, ?, 'test-fixture', ?)
		ON CONFLICT(id) DO UPDATE SET
			controller_application_id = excluded.controller_application_id,
			selection_reason = excluded.selection_reason,
			selected_at = excluded.selected_at`, applicationID, now); err != nil {
		t.Fatal(err)
	}
}

func configureBuiltinHeadscaleForTest(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := store.putSecret(ctx, tx, []byte("test-headscale-api-key"), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.example.test', ?, 'configured', ?, ?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func enrollOrchestrationNode(t *testing.T, store *Store, name string, capabilities NodeCapabilities, candidates []networking.Candidate, profile networking.Profile) AgentCredential {
	t.Helper()
	ctx := context.Background()
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: name, CenterURL: "https://center.example.com", Gateway: capabilities.Gateway, Tunnel: capabilities.Tunnel})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	if capabilities.Docker || capabilities.Tunnel {
		roles = append(roles, "worker")
	}
	if capabilities.Gateway {
		roles = append(roles, "gateway")
	}
	var publicEgress *networking.PublicEgress
	if profile.DirectPublic {
		profile.PublicMode = networking.PublicModeDirect
		profile.PublicBindAddress = profile.PublicAddress
		publicEgress = &networking.PublicEgress{Address: profile.PublicAddress, BindAddress: profile.PublicAddress, Mode: networking.PublicModeDirect, ObservedAt: store.now().UTC()}
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: roles, Capabilities: capabilities, NetworkCandidates: candidates, PublicEgress: publicEgress, GatewayHealthy: capabilities.Gateway, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, Startup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmNetworkProfile(ctx, node.ID, profile); err != nil {
		t.Fatal(err)
	}
	return node
}
