package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestCPACredentialLifecycleGeneratesPreservesRevealsAndRotates(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sites SET timezone = 'Asia/Singapore' WHERE id = ?`, testSiteID(t, store)); err != nil {
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "cpa-credentials", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})

	install, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, node)
	var installConfig map[string]any
	var generated cpaCredentialValues
	if json.Unmarshal(installTask.Config, &installConfig) != nil || json.Unmarshal(installTask.Secrets, &generated) != nil {
		t.Fatalf("CPA install task is invalid: %#v", installTask)
	}
	if installConfig["timezone"] != "Asia/Singapore" || generated.ManagementKey == "" || generated.APIKey == "" || generated.ManagementKey == generated.APIKey {
		t.Fatalf("CPA automatic configuration = %#v, credentials=%#v", installConfig, generated)
	}
	completeApplicationTaskForCredentialTest(t, store, node, installTask, "10.0.0.95", 8317)

	if _, err := store.RevealApplicationCredentials(ctx, install.ApplicationID, adminID, "wrong-password-value"); err == nil || !strings.Contains(err.Error(), "current password is incorrect") {
		t.Fatalf("CPA reveal accepted failed reauthentication: %v", err)
	}
	revealed, err := store.RevealApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple")
	if err != nil || revealed.Kind != "cpa" || revealed.ManagementKey != generated.ManagementKey || revealed.ClientAPIKey != generated.APIKey {
		t.Fatalf("CPA reveal = %#v err=%v", revealed, err)
	}
	revealBody, _ := json.Marshal(map[string]string{"currentPassword": "correct-horse-battery-staple"})
	revealRequest := httptest.NewRequest(http.MethodPost, "/api/v1/applications/"+install.ApplicationID+"/credentials/reveal", bytes.NewReader(revealBody))
	revealRequest.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	revealRequest.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
	revealRequest.Header.Set("Content-Type", "application/json")
	revealRequest.Header.Set("X-CSRF-Token", csrf)
	revealResponse := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(revealResponse, revealRequest)
	if revealResponse.Code != http.StatusOK || revealResponse.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(revealResponse.Body.Bytes(), []byte(generated.ManagementKey)) {
		t.Fatalf("CPA reveal response status=%d cache=%q body=%q", revealResponse.Code, revealResponse.Header().Get("Cache-Control"), revealResponse.Body.String())
	}

	configure, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{"debug":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	configureTask := claimTask(t, store, node)
	var preserved cpaCredentialValues
	if json.Unmarshal(configureTask.Secrets, &preserved) != nil || preserved != generated {
		t.Fatalf("ordinary CPA configure changed credentials: got=%#v want=%#v", preserved, generated)
	}
	completeApplicationTaskForCredentialTest(t, store, node, configureTask, "10.0.0.95", 8317)
	if configure.ID == install.ID {
		t.Fatal("configure reused the install deployment")
	}

	clientRotation, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "client-rotation-operation-0001", "client", true)
	if err != nil || clientRotation.State != "pending" {
		t.Fatalf("client rotation = %#v err=%v", clientRotation, err)
	}
	visibleRotation, err := store.ApplicationCredentialRotation(ctx, install.ApplicationID, clientRotation.ID)
	if err != nil || visibleRotation.ID != clientRotation.ID || visibleRotation.State != "pending" {
		t.Fatalf("visible client rotation = %#v err=%v", visibleRotation, err)
	}
	if _, err := store.ApplicationCredentialRotation(ctx, "another-application", clientRotation.ID); err == nil {
		t.Fatal("credential rotation status crossed its application boundary")
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/applications/"+install.ApplicationID+"/credentials/rotations/"+clientRotation.ID, nil)
	statusRequest.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	statusResponse := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || statusResponse.Header().Get("Cache-Control") != "no-store" || bytes.Contains(statusResponse.Body.Bytes(), []byte(generated.APIKey)) {
		t.Fatalf("CPA rotation status response status=%d cache=%q body=%q", statusResponse.Code, statusResponse.Header().Get("Cache-Control"), statusResponse.Body.String())
	}
	clientTask := claimTask(t, store, node)
	var clientValues cpaCredentialValues
	if json.Unmarshal(clientTask.Secrets, &clientValues) != nil || clientValues.ManagementKey != generated.ManagementKey || clientValues.APIKey == generated.APIKey || clientValues.APIKey == "" {
		t.Fatalf("client rotation values = %#v", clientValues)
	}
	completeApplicationTaskForCredentialTest(t, store, node, clientTask, "10.0.0.95", 8317)
	replayedClient, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "client-rotation-operation-0001", "client", true)
	if err != nil || replayedClient.ID != clientRotation.ID || replayedClient.State != "succeeded" {
		t.Fatalf("client rotation replay = %#v err=%v", replayedClient, err)
	}

	keeper, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/keeper", Config: json.RawMessage(`{"timezone":"Asia/Singapore","login_password":"keeper-password-value"}`)})
	if err != nil {
		t.Fatal(err)
	}
	keeperInstallTask := claimTask(t, store, node)
	completeApplicationTaskForCredentialTest(t, store, node, keeperInstallTask, "10.0.0.95", 8080)
	if keeper.ApplicationID == "" {
		t.Fatal("Keeper application was not created")
	}

	managementRotation, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "management-rotation-operation-1", "management", true)
	if err != nil || managementRotation.State != "pending" || managementRotation.KeeperDeploymentID == "" {
		t.Fatalf("management rotation = %#v err=%v", managementRotation, err)
	}
	managementTask := claimTask(t, store, node)
	var managementValues cpaCredentialValues
	if json.Unmarshal(managementTask.Secrets, &managementValues) != nil || managementValues.ManagementKey == clientValues.ManagementKey || managementValues.APIKey != clientValues.APIKey {
		t.Fatalf("management rotation values = %#v", managementValues)
	}
	completeApplicationTaskForCredentialTest(t, store, node, managementTask, "10.0.0.95", 8317)
	keeperRotationTask := claimTask(t, store, node)
	var keeperSecrets map[string]string
	if keeperRotationTask.AppKey != "vastora-official/keeper" || json.Unmarshal(keeperRotationTask.Secrets, &keeperSecrets) != nil || keeperSecrets["cpa_management_key"] != managementValues.ManagementKey {
		t.Fatalf("Keeper did not receive the rotated CPA key: %#v secrets=%#v", keeperRotationTask, keeperSecrets)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, keeperRotationTask.ID, keeperRotationTask.Attempt, false, "simulated Keeper rejection", nil, keeperRotationTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	blockedRotation, err := store.ApplicationCredentialRotation(ctx, install.ApplicationID, managementRotation.ID)
	if err != nil || blockedRotation.State != "action_required" {
		t.Fatalf("partial management rotation = %#v err=%v", blockedRotation, err)
	}
	if _, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "new-management-operation", "management", true); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("action-required rotation did not block a second value: %v", err)
	}
	retriedManagement, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "management-rotation-operation-1", "management", true)
	if err != nil || retriedManagement.ID != managementRotation.ID || retriedManagement.State != "pending" || retriedManagement.KeeperDeploymentID == keeperRotationTask.ID {
		t.Fatalf("management rotation retry = %#v err=%v", retriedManagement, err)
	}
	keeperRetryTask := claimTask(t, store, node)
	var keeperRetrySecrets map[string]string
	if json.Unmarshal(keeperRetryTask.Secrets, &keeperRetrySecrets) != nil || keeperRetrySecrets["cpa_management_key"] != managementValues.ManagementKey {
		t.Fatalf("Keeper retry did not reuse the committed rotated key: %#v", keeperRetrySecrets)
	}
	completeApplicationTaskForCredentialTest(t, store, node, keeperRetryTask, "10.0.0.95", 8080)
	completedManagement, err := store.RotateApplicationCredentials(ctx, install.ApplicationID, adminID, "correct-horse-battery-staple", "management-rotation-operation-1", "management", true)
	if err != nil || completedManagement.ID != managementRotation.ID || completedManagement.State != "succeeded" {
		t.Fatalf("management rotation completion = %#v err=%v", completedManagement, err)
	}
	var retainedRotationSecrets int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_credential_rotations WHERE state = 'succeeded' AND secret_id IS NOT NULL`).Scan(&retainedRotationSecrets); err != nil || retainedRotationSecrets != 0 {
		t.Fatalf("completed rotations retained transient secrets: count=%d err=%v", retainedRotationSecrets, err)
	}

	actions, err := store.ListActions(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(actions)
	for _, secretValue := range []string{generated.ManagementKey, generated.APIKey, clientValues.APIKey, managementValues.ManagementKey} {
		if bytes.Contains(encoded, []byte(secretValue)) {
			t.Fatalf("credential leaked into audit actions: %s", encoded)
		}
	}
}

func completeApplicationTaskForCredentialTest(t *testing.T, store *Store, node AgentCredential, task *AgentTask, address string, port int) {
	t.Helper()
	result, _ := json.Marshal(ApplicationTaskResult{Services: []ApplicationServiceResult{{Name: credentialTestServiceName(task.AppKey), Protocol: "http", ContainerPort: port, HostPort: port, Address: address}}})
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
}

func credentialTestServiceName(appKey string) string {
	if appKey == "vastora-official/keeper" {
		return "dashboard"
	}
	return "api"
}
