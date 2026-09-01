package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestDeploymentCredentialsReplayAcrossConcurrencyAndRestartUntilAcknowledged(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	catalogPayload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(ctx, catalogPayload); err != nil {
		store.Close()
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "secret-replay", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
	request := DeploymentRequest{
		AgentID:              node.ID,
		AppKey:               threeXUIAppKey,
		Role:                 threeXUIRoleMaster,
		Config:               json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`),
		Operation:            "install",
		SecretOperationOwner: "test-admin-session",
		SecretOperationKey:   "deployment-operation-key-0001",
	}

	start := make(chan struct{})
	results := make([]DeploymentView, 2)
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsByCall[index] = store.CreateDeployment(ctx, request)
		}(index)
	}
	close(start)
	wait.Wait()
	for index, createErr := range errorsByCall {
		if createErr != nil {
			store.Close()
			t.Fatalf("concurrent create %d: %v", index, createErr)
		}
	}
	if results[0].ID != results[1].ID || results[0].OneTimeCredentials == nil || results[1].OneTimeCredentials == nil || *results[0].OneTimeCredentials != *results[1].OneTimeCredentials {
		store.Close()
		t.Fatalf("same operation did not replay one credential result: %#v %#v", results[0], results[1])
	}
	credentials := *results[0].OneTimeCredentials
	changed := request
	changed.Config = json.RawMessage(`{"timezone":"UTC","panel_port":2054,"enable_fail2ban":true,"vmess_aead_forced":false}`)
	if _, err := store.CreateDeployment(ctx, changed); err == nil || !strings.Contains(err.Error(), "different operation") {
		store.Close()
		t.Fatalf("same key accepted a changed deployment: %v", err)
	}
	differentKey := request
	differentKey.SecretOperationKey = "deployment-operation-key-0002"
	if _, err := store.CreateDeployment(ctx, differentKey); err == nil || !strings.Contains(err.Error(), "already has a 3x-ui controller") {
		store.Close()
		t.Fatalf("different key bypassed the deployment conflict: %v", err)
	}
	listed, err := store.ListDeployments(ctx)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(listed)
	if len(listed) != 1 || !listed[0].OneTimeCredentialsAvailable || listed[0].OneTimeCredentials != nil || bytes.Contains(encoded, []byte(credentials.Username)) || bytes.Contains(encoded, []byte(credentials.Password)) {
		store.Close()
		t.Fatalf("deployment list exposed credentials or hid recoverability: %s", encoded)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := store.CreateDeployment(ctx, request)
	if err != nil || replayed.ID != results[0].ID || replayed.OneTimeCredentials == nil || *replayed.OneTimeCredentials != credentials {
		t.Fatalf("deployment credentials did not survive restart: %#v err=%v", replayed, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		revealed, err := store.RevealDeploymentCredentials(ctx, replayed.ID, request.SecretOperationOwner, request.SecretOperationKey)
		if err != nil || revealed != credentials {
			t.Fatalf("credential reveal %d = %#v err=%v", attempt, revealed, err)
		}
	}
	if err := store.AcknowledgeDeploymentCredentials(ctx, replayed.ID, request.SecretOperationOwner, request.SecretOperationKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevealDeploymentCredentials(ctx, replayed.ID, request.SecretOperationOwner, request.SecretOperationKey); err == nil {
		t.Fatal("acknowledged deployment credentials remained revealable")
	}
	if _, err := store.CreateDeployment(ctx, request); err == nil || !strings.Contains(err.Error(), "acknowledged") {
		t.Fatalf("acknowledged deployment operation replayed credentials: %v", err)
	}
}

func TestStoredThreeXUICredentialsRequireAdministratorReauthenticationAndAreAudited(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	catalogPayload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(ctx, catalogPayload); err != nil {
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "stored-credential-reveal", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	request := DeploymentRequest{
		AgentID:              node.ID,
		AppKey:               threeXUIAppKey,
		Role:                 threeXUIRoleMaster,
		Config:               json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`),
		Operation:            "install",
		SecretOperationOwner: adminID,
		SecretOperationKey:   "stored-credential-operation-key",
	}
	deployment, err := store.CreateDeployment(ctx, request)
	if err != nil || deployment.OneTimeCredentials == nil {
		t.Fatalf("create deployment = %#v err=%v", deployment, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded' WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeDeploymentCredentials(ctx, deployment.ID, adminID, request.SecretOperationKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevealStoredThreeXUICredentials(ctx, deployment.ApplicationID, adminID, "incorrect-password"); err == nil || !strings.Contains(err.Error(), "current password is incorrect") {
		t.Fatalf("stored credentials accepted incorrect reauthentication: %v", err)
	}
	revealed, err := store.RevealStoredThreeXUICredentials(ctx, deployment.ApplicationID, adminID, "correct-horse-battery-staple")
	if err != nil || revealed != *deployment.OneTimeCredentials {
		t.Fatalf("stored credentials = %#v err=%v", revealed, err)
	}
	actions, err := store.ListActions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	encodedActions, _ := json.Marshal(actions)
	if len(actions) == 0 || actions[0].Kind != "security.credentials.reveal" || actions[0].TaskID != deployment.ApplicationID {
		t.Fatalf("credential reveal audit event missing: %#v", actions)
	}
	if bytes.Contains(encodedActions, []byte(revealed.Username)) || bytes.Contains(encodedActions, []byte(revealed.Password)) {
		t.Fatalf("credential reveal audit event exposed credentials: %s", encodedActions)
	}
}

func TestApplicationResultSurvivesFailedResponseAndRestartUntilAcknowledged(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	catalogPayload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(ctx, catalogPayload); err != nil {
		store.Close()
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "response-replay", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	const applicationID = "secret-response-application"
	const commandID = "secret-response-command"
	const secretValue = "vless://private-result-that-must-survive-response-loss"
	inputJSON, err := json.Marshal(ThreeXUIClientCommandTask{Action: "reveal_link", Email: "MacBook", InboundID: 9})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES(?, '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, applicationID, node.ID, testSiteID(t, store), threeXUIAppKey, now, now); err != nil {
		store.Close()
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte(secretValue), "application-command:"+commandID)
	if err != nil {
		tx.Rollback()
		store.Close()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, result_json, result_secret_id, state, created_at, updated_at) VALUES(?, ?, ?, ?, '3xui.clients.manage', ?, '{}', ?, 'succeeded', ?, ?)`, commandID, applicationID, node.ID, node.ID, inputJSON, secretID, now, now); err != nil {
		tx.Rollback()
		store.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	const operationKey = "command-operation-key-0001"
	handler := NewServer(store, "", false).Handler()
	failedWriter := &responseWriteFailure{header: make(http.Header)}
	handler.ServeHTTP(failedWriter, authenticatedSecretRequest(http.MethodPost, "/api/v1/application-commands/"+commandID+"/reveal", session, csrf, operationKey))
	if failedWriter.status != http.StatusOK || failedWriter.writeErr == nil {
		store.Close()
		t.Fatalf("failed response did not reach the reveal body: status=%d err=%v", failedWriter.status, failedWriter.writeErr)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler = NewServer(store, "", false).Handler()
	var wait sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 2)
	for index := range responses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			responses[index] = httptest.NewRecorder()
			handler.ServeHTTP(responses[index], authenticatedSecretRequest(http.MethodPost, "/api/v1/application-commands/"+commandID+"/reveal", session, csrf, operationKey))
		}(index)
	}
	wait.Wait()
	for index, response := range responses {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), secretValue) {
			t.Fatalf("replayed response %d status=%d body=%q", index, response.Code, response.Body.String())
		}
	}
	command, err := store.ApplicationCommand(ctx, commandID)
	if err != nil || !command.ResultAvailable {
		t.Fatalf("response replay consumed the secret before acknowledgement: %#v err=%v", command, err)
	}
	encoded, _ := json.Marshal(command)
	if bytes.Contains(encoded, []byte(secretValue)) {
		t.Fatalf("public application command leaked its result: %s", encoded)
	}
	acknowledged := httptest.NewRecorder()
	handler.ServeHTTP(acknowledged, authenticatedSecretRequest(http.MethodPost, "/api/v1/application-commands/"+commandID+"/ack", session, csrf, operationKey))
	if acknowledged.Code != http.StatusOK {
		t.Fatalf("acknowledge status=%d body=%q", acknowledged.Code, acknowledged.Body.String())
	}
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, authenticatedSecretRequest(http.MethodPost, "/api/v1/application-commands/"+commandID+"/reveal", session, csrf, operationKey))
	if denied.Code != http.StatusBadRequest || strings.Contains(denied.Body.String(), secretValue) {
		t.Fatalf("acknowledged result was disclosed: status=%d body=%q", denied.Code, denied.Body.String())
	}
}

type responseWriteFailure struct {
	header   http.Header
	status   int
	writeErr error
}

func (writer *responseWriteFailure) Header() http.Header { return writer.header }

func (writer *responseWriteFailure) WriteHeader(status int) { writer.status = status }

func (writer *responseWriteFailure) Write(_ []byte) (int, error) {
	writer.writeErr = errors.New("simulated client disconnect")
	return 0, writer.writeErr
}

func authenticatedSecretRequest(method, target, session, csrf, operationKey string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	request.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", operationKey)
	return request
}
