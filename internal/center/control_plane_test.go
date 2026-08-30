package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func testAgentPublicKey(t *testing.T) []byte {
	t.Helper()
	_, publicKey, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return publicKey
}

func TestDecodeJSONRejectsPayloadLargerThanLimit(t *testing.T) {
	request := httptest.NewRequest("POST", "/", bytes.NewReader(append([]byte(`{"value":"`), bytes.Repeat([]byte("a"), (1<<20)+1)...)))
	request.Header.Set("Content-Type", "application/json")
	var target map[string]string
	if err := decodeJSON(request, &target); err == nil || !strings.Contains(err.Error(), "exceeds the allowed size") {
		t.Fatalf("oversized JSON error = %v", err)
	}
}

func TestEnrollmentTokenIsConsumedOnceUnderConcurrency(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "one-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, publicKey, keyErr := controlplane.GenerateKeyPair()
			if keyErr != nil {
				return
			}
			if _, enrollErr := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", publicKey); enrollErr == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent enrollment successes = %d, want 1", successes.Load())
	}
}

func TestEncryptedAgentTaskAndImmediateCredentialRevocation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	privateKey, publicKey, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "encrypted-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	task := AgentTask{Kind: "application.apply", ID: "task-1", Attempt: 2, Secrets: json.RawMessage(`{"password":"private"}`)}
	encrypted, err := store.EncryptAgentTask(context.Background(), credential.ID, task)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := controlplane.Open(privateKey, encrypted.Envelope, controlplane.TaskAdditionalData(credential.ID, task.ID, task.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	var decoded AgentTask
	if json.Unmarshal(plaintext, &decoded) != nil || decoded.ID != task.ID || string(decoded.Secrets) != string(task.Secrets) {
		t.Fatalf("decrypted task = %#v", decoded)
	}
	if err := store.RevokeAgentCredential(context.Background(), credential.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.authenticateAgent(context.Background(), credential.ID, credential.Credential); err == nil {
		t.Fatal("revoked Agent authenticated")
	}
	if _, err := store.EncryptAgentTask(context.Background(), credential.ID, task); err == nil {
		t.Fatal("revoked Agent received an encrypted task")
	}
}

func TestRevocationInterruptsLongPollAndRejectsAcknowledgement(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "revoked-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, pollErr := store.WaitAndClaimNextTask(context.Background(), credential.ID, credential.Credential, 30*time.Second)
		result <- pollErr
	}()
	if err := store.RevokeAgentCredential(context.Background(), credential.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case pollErr := <-result:
		if pollErr == nil || !strings.Contains(pollErr.Error(), "authentication failed") {
			t.Fatalf("revoked long poll error = %v", pollErr)
		}
	case <-time.After(time.Second):
		t.Fatal("credential revocation did not interrupt the active long poll")
	}
	if err := store.CompleteTask(context.Background(), credential.ID, credential.Credential, "missing-task", 1, true, "", nil); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("revoked Agent acknowledgement error = %v", err)
	}
}

func TestLegacyAgentCannotLeaseTaskBeforeEncryptionIdentityBackfill(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "legacy-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET x25519_public_key = X'' WHERE id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("legacy Agent claim error = %v", err)
	}
	var state string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM deployments WHERE id = ?`, deployment.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("task was leased before encryption identity backfill: state=%q err=%v", state, err)
	}
	publicKey := testAgentPublicKey(t)
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{PublicKey: publicKey, Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task == nil || task.ID != deployment.ID {
		t.Fatalf("task was not claimable after encryption identity backfill: task=%#v err=%v", task, err)
	}
}

func TestDeploymentLeasesBoundRegistryCredentialOnlyToMatchingImageTask(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "registry-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.31", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", EnabledKinds: []string{networking.KindLAN}})
	credential, err := store.CreateRegistryCredential(ctx, "docker.io", "robot", "private-registry-token")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID: node.ID, AppKey: cpaAppKey, RegistryCredentialID: &credential.ID,
		Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.ID != deployment.ID || task.RegistryCredential == nil {
		t.Fatalf("claimed task did not include its Registry binding: %#v", task)
	}
	if task.RegistryCredential.Host != "docker.io" || task.RegistryCredential.Username != "robot" || task.RegistryCredential.Password != "private-registry-token" {
		t.Fatalf("unexpected Registry credential lease: %#v", task.RegistryCredential)
	}
	other, err := store.CreateRegistryCredential(ctx, "registry.example.test:5443", "robot", "other-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: komariAppKey, RegistryCredentialID: &other.ID, Config: json.RawMessage(`{"endpoint":"https://komari.example.test","token":"token"}`)}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("credential for a different Registry was accepted: %v", err)
	}
}

func TestRegistryCredentialBindingCanBePreservedAndExplicitlyCleared(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "registry-binding-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.32", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.32", LANAddress: "10.0.0.32", EnabledKinds: []string{networking.KindLAN}})
	credential, err := store.CreateRegistryCredential(ctx, "docker.io", "robot", "write-only-token")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, RegistryCredentialID: &credential.ID, Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded' WHERE id = ?`, initial.ID); err != nil {
		t.Fatal(err)
	}
	preserved, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{"debug":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	var bound string
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(registry_credential_id, '') FROM deployments WHERE id = ?`, preserved.ID).Scan(&bound); err != nil || bound != credential.ID {
		t.Fatalf("omitted binding was not preserved: binding=%q err=%v", bound, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded' WHERE id = ?`, preserved.ID); err != nil {
		t.Fatal(err)
	}
	clear := ""
	cleared, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", RegistryCredentialID: &clear, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(registry_credential_id, '') FROM deployments WHERE id = ?`, cleared.ID).Scan(&bound); err != nil || bound != "" {
		t.Fatalf("explicit empty binding was not cleared: binding=%q err=%v", bound, err)
	}
	metadata, err := store.ListRegistryCredentials(ctx)
	if err != nil || len(metadata) != 1 || metadata[0].ID != credential.ID || !metadata[0].TokenSet {
		t.Fatalf("redacted Registry metadata = %#v err=%v", metadata, err)
	}
	encoded, _ := json.Marshal(metadata)
	if bytes.Contains(encoded, []byte("write-only-token")) {
		t.Fatal("Registry metadata exposed its token")
	}
}

func TestRegistryCredentialRotationAndSafeDeletion(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "registry-lifecycle-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.33", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.33", LANAddress: "10.0.0.33", EnabledKinds: []string{networking.KindLAN}})
	credential, err := store.CreateRegistryCredential(ctx, "docker.io", "robot-old", "old-token")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := store.RotateRegistryCredential(ctx, credential.ID, "robot-new", "new-token")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != credential.ID || rotated.Host != credential.Host || rotated.Username != "robot-new" {
		t.Fatalf("rotated credential = %#v", rotated)
	}
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, RegistryCredentialID: &credential.ID, Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateRegistryCredential(ctx, credential.ID, "robot-pending", "pending-token"); err == nil || !strings.Contains(err.Error(), "cannot rotate") {
		t.Fatalf("pending deployment allowed Registry rotation: %v", err)
	}
	task := claimTask(t, store, node)
	if task.RegistryCredential == nil || task.RegistryCredential.Username != "robot-new" || task.RegistryCredential.Password != "new-token" {
		t.Fatalf("rotated Registry lease = %#v", task.RegistryCredential)
	}
	if _, err := store.RotateRegistryCredential(ctx, credential.ID, "robot-running", "running-token"); err == nil || !strings.Contains(err.Error(), "cannot rotate") {
		t.Fatalf("running deployment allowed Registry rotation: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'failed', reconciliation_required = 1 WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateRegistryCredential(ctx, credential.ID, "robot-reconcile", "reconcile-token"); err == nil || !strings.Contains(err.Error(), "cannot rotate") {
		t.Fatalf("reconciling deployment allowed Registry rotation: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET reconciliation_required = 0 WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateRegistryCredential(ctx, credential.ID, "robot-final", "final-token"); err != nil {
		t.Fatalf("terminal deployment blocked Registry rotation: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded' WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRegistryCredential(ctx, credential.ID); err == nil || !strings.Contains(err.Error(), "still bound") {
		t.Fatalf("active Registry binding deletion = %v", err)
	}
	clear := ""
	cleared, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", RegistryCredentialID: &clear, Config: json.RawMessage(`{"debug":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded' WHERE id = ?`, cleared.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRegistryCredential(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	if credentials, err := store.ListRegistryCredentials(ctx); err != nil || len(credentials) != 0 {
		t.Fatalf("credentials after deletion = %#v, err=%v", credentials, err)
	}
}
