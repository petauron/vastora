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
	"github.com/petauron/vastora/internal/platform"
)

func TestThreeXUIDeploymentCanBeQuarantinedAndRetriedWithItsSecrets(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "reconciliation-deployment", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: "10.0.0.14", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "10.0.0.24", Interface: "eth1", Kind: networking.KindLAN},
	}, networking.Profile{ServiceAddress: "10.0.0.14", LANAddress: "10.0.0.14", EnabledKinds: []string{networking.KindLAN}})
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	result := json.RawMessage(`{"generatedSecrets":{"api_token":"recovered-local-api-token"}}`)
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "container state requires reconciliation", result, true, platform.ApplicationRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	deployments, err := store.ListDeployments(ctx)
	if err != nil || len(deployments) != 1 || deployments[0].ID != task.ID || deployments[0].State != "failed" || !deployments[0].ReconciliationRequired {
		t.Fatalf("quarantined deployment is not visible: %#v err=%v", deployments, err)
	}
	secretTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, secretErr := store.threeXUIAPISecret(ctx, secretTx, created.ApplicationID)
	secretTx.Rollback()
	if secretErr != nil || apiToken != "recovered-local-api-token" {
		t.Fatalf("generated API token was not retained: token=%q err=%v", apiToken, secretErr)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)}); err == nil || !strings.Contains(err.Error(), "active deployment task") {
		t.Fatalf("quarantined deployment did not keep the task lock: %v", err)
	}
	if _, err := store.ConfirmNetworkProfile(ctx, node.ID, networking.Profile{ServiceAddress: "10.0.0.24", LANAddress: "10.0.0.24", EnabledKinds: []string{networking.KindLAN}}); err == nil || !strings.Contains(err.Error(), "recover deployment tasks") {
		t.Fatalf("quarantined deployment allowed its captured service address to diverge: %v", err)
	}
	retry, err := store.RetryTaskReconciliation(ctx, task.ID)
	if err != nil || !retry.Queued || retry.TaskID != task.ID || retry.Kind != "application.apply" {
		t.Fatalf("deployment reconciliation was not retried in place: %#v err=%v", retry, err)
	}
	var state, taskError, applicationStatus string
	var attempt int64
	var reconciliationRequired int
	if err := store.db.QueryRowContext(ctx, `SELECT deployment.state, deployment.reconciliation_required, deployment.attempt, deployment.error, application.status FROM deployments deployment JOIN applications application ON application.id = deployment.application_id WHERE deployment.id = ?`, task.ID).Scan(&state, &reconciliationRequired, &attempt, &taskError, &applicationStatus); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || reconciliationRequired != 0 || attempt != task.Attempt || taskError != "" || applicationStatus != "pending" {
		t.Fatalf("unexpected retried deployment state: state=%q reconciliation=%d attempt=%d error=%q application=%q", state, reconciliationRequired, attempt, taskError, applicationStatus)
	}
	replayed := claimTask(t, store, node)
	if replayed.ID != task.ID || replayed.Attempt != task.Attempt+1 || !replayed.Reconcile {
		t.Fatalf("reconciliation created a new task instead of replaying it: first=%#v replay=%#v", task, replayed)
	}
}

func TestTaskEncryptionFailureReleasesTheCommittedLease(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "encryption-race", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if err := store.RevokeAgentCredential(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EncryptAgentTask(ctx, node.ID, task); err == nil {
		t.Fatal("revoked Agent encryption identity remained usable")
	}
	if err := store.releaseClaimedTask(ctx, node.ID, task); err != nil {
		t.Fatal(err)
	}
	var state, lease, applicationStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT deployment.state, deployment.lease_expires_at, application.status FROM deployments deployment JOIN applications application ON application.id = deployment.application_id WHERE deployment.id = ?`, created.ID).Scan(&state, &lease, &applicationStatus); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || lease != "" || applicationStatus != "pending" {
		t.Fatalf("released task state=%q lease=%q application=%q", state, lease, applicationStatus)
	}
}

func TestApplicationCommandQuarantineLocksAndAuthenticatedRetryReplaysSameTask(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "reconciliation-command", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.15", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.15", LANAddress: "10.0.0.15", EnabledKinds: []string{networking.KindLAN}})
	applicationID := "three-x-ui-reconciliation-command"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES(?, '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, applicationID, node.ID, testSiteID(t, store), threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	commandID := "application-command-reconciliation-replay"
	input, _ := json.Marshal(ThreeXUIClientCommandTask{Action: "list", Inbounds: []ThreeXUIClientInbound{}})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, commandID, applicationID, node.ID, node.ID, clientCommandKind, input, now, now); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "remote API result is uncertain", nil, true); err != nil {
		t.Fatal(err)
	}
	command, err := store.ApplicationCommand(ctx, commandID)
	if err != nil || command.State != "failed" || !command.ReconciliationRequired {
		t.Fatalf("quarantined command is not visible: %#v err=%v", command, err)
	}
	if _, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: applicationID, Action: "list"}); err == nil || !strings.Contains(err.Error(), "operation in progress") {
		t.Fatalf("quarantined command did not retain the application operation lock: %v", err)
	}

	server := NewServer(store, "", false).Handler()
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+commandID+"/retry-reconciliation", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated reconciliation retry status = %d", unauthorized.Code)
	}
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	authorizedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+commandID+"/retry-reconciliation", nil)
	authorizedRequest.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	authorizedRequest.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
	authorizedRequest.Header.Set("X-CSRF-Token", csrf)
	authorized := httptest.NewRecorder()
	server.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusAccepted {
		t.Fatalf("authorized reconciliation retry status = %d body=%q", authorized.Code, authorized.Body.String())
	}
	command, err = store.ApplicationCommand(ctx, commandID)
	if err != nil || command.State != "pending" || command.ReconciliationRequired || command.Error != "" {
		t.Fatalf("retried command was not reset safely: %#v err=%v", command, err)
	}
	replayed := claimTask(t, store, node)
	if replayed.ID != commandID || replayed.Attempt != task.Attempt+1 || !replayed.Reconcile {
		t.Fatalf("command reconciliation did not preserve task identity and attempt history: first=%#v replay=%#v", task, replayed)
	}
}

func TestRealityDisplayNameReservationSpansAgentsUntilTerminalCompensation(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	controller := enrollOrchestrationNode(t, store, "current-controller", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.17", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.17", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.17", LANAddress: "10.0.0.17", PublicAddress: "203.0.113.17", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	previousController := enrollOrchestrationNode(t, store, "previous-controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: controller.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, controller)
	completeThreeXUIDeployment(t, store, controller, installTask, "10.0.0.17", "controller-api-token")

	create := func(name string) (ApplicationCommandView, error) {
		return store.CreateRealityCommand(ctx, RealityCommandInput{
			ApplicationID: deployment.ApplicationID,
			RegionCode:    "US",
			Name:          name,
			ClientName:    "Phone",
			GatewayNodeID: controller.ID,
			Hostname:      "reality-reservation.example.test",
			DNSProvider:   "manual",
			TargetHost:    "www.example.com",
			ServerName:    "www.example.com",
		})
	}
	command, err := create("Edge")
	if err != nil {
		t.Fatal(err)
	}
	// A controller handoff can leave the same durable command owned by the old
	// Agent. The Site-level reservation must outlive that ownership change.
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET agent_id = ? WHERE id = ?`, previousController.ID, command.ID); err != nil {
		t.Fatal(err)
	}
	assertReserved := func(stage string) {
		t.Helper()
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
			WHERE site_id = ? AND display_name = ? COLLATE NOCASE
			AND kind IN (?, ?) AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, testSiteID(t, store), "🇺🇸 美国edge", realityCommandKind, realityRenameCommandKind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s reservation count = %d, want 1", stage, count)
		}
		if _, err := create("edge"); err == nil || !strings.Contains(err.Error(), "reserving that node name") {
			t.Fatalf("%s reservation did not reject the current Agent: %v", stage, err)
		}
	}
	assertReserved("pending cross-Agent")

	first := claimTask(t, store, previousController)
	if first.ID != command.ID {
		t.Fatalf("old Agent claimed command %q, want %q", first.ID, command.ID)
	}
	if err := store.completeTaskWithDisposition(ctx, previousController.ID, previousController.Credential, first.ID, first.Attempt, false, "remote API state is uncertain", nil, true); err != nil {
		t.Fatal(err)
	}
	assertReserved("reconciliation")

	retry, err := store.RetryTaskReconciliation(ctx, command.ID)
	if err != nil || !retry.Queued || retry.TaskID != command.ID {
		t.Fatalf("reconciliation retry = %#v, err=%v", retry, err)
	}
	replayed := claimTask(t, store, previousController)
	if replayed.ID != command.ID || replayed.Attempt != first.Attempt+1 || !replayed.Reconcile {
		t.Fatalf("reconciliation changed task identity or attempt history: first=%#v replay=%#v", first, replayed)
	}
	if err := store.CompleteTask(ctx, previousController.ID, previousController.Credential, replayed.ID, replayed.Attempt, false, "remote mutation was compensated", nil, replayed.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var state string
	var reconciliationRequired, reservations int
	if err := store.db.QueryRowContext(ctx, `SELECT state, reconciliation_required FROM application_commands WHERE id = ?`, command.ID).Scan(&state, &reconciliationRequired); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
		WHERE site_id = ? AND display_name = ? COLLATE NOCASE
		AND kind IN (?, ?) AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, testSiteID(t, store), "🇺🇸 美国edge", realityCommandKind, realityRenameCommandKind).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reconciliationRequired != 0 || reservations != 0 {
		t.Fatalf("terminal compensation did not release reservation: state=%q reconciliation=%d reservations=%d", state, reconciliationRequired, reservations)
	}
	if released, err := create("edge"); err != nil || released.ID == "" || released.ID == command.ID {
		t.Fatalf("released display name was not reusable by the current Agent: command=%#v err=%v", released, err)
	}
}

func TestInvalidReconciliationDispositionIsRejected(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "invalid-reconciliation", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.16", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.16", LANAddress: "10.0.0.16", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: config}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "uncertain non-3x-ui state", nil, true); !errors.Is(err, errInvalidReconciliationDisposition) {
		t.Fatalf("non-3x-ui task accepted reconciliation disposition: %v", err)
	}
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, true); !errors.Is(err, errInvalidReconciliationDisposition) {
		t.Fatalf("successful task accepted reconciliation disposition: %v", err)
	}
	if err := store.completeTaskWithDisposition(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "", nil, true); !errors.Is(err, errInvalidReconciliationDisposition) {
		t.Fatalf("reconciliation disposition without an error was accepted: %v", err)
	}
}

func TestExpiredTaskIsRetriedAndStaleResultIsRejected(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err != nil {
		t.Fatal(err)
	}
	first := claimTask(t, store, node)
	clock = clock.Add(taskLeaseDuration + time.Second)
	second := claimTask(t, store, node)
	if first.ID != second.ID || first.Attempt != 1 || second.Attempt != 2 || first.Revision != second.Revision {
		t.Fatalf("unexpected retry claims: first=%#v second=%#v", first, second)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, first.ID, first.Attempt, false, "late result", nil, first.RequiredRuntimeGeneration); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale result was not rejected: %v", err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, second.ID, second.Attempt, false, "expected failure", nil, second.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	actions, err := store.ListActions(ctx, defaultActionLimit)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]int{}
	for _, action := range actions {
		if action.TaskID == first.ID {
			events[action.Event]++
		}
	}
	if events["queued"] != 1 || events["claimed"] != 2 || events["lease_expired"] != 1 || events["failed"] != 1 {
		t.Fatalf("unexpected task audit trail: %#v", events)
	}
	deployments, err := store.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 || deployments[0].State != "failed" || deployments[0].Error != "expected failure" || deployments[0].OneTimeCredentials != nil {
		t.Fatalf("failed operation is not safely visible to the UI: %#v", deployments)
	}
}

func TestTaskLeaseRenewalKeepsAttemptActiveAndNeverResurrectsExpiredLease(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "lease-renewal", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: config}); err != nil {
		t.Fatal(err)
	}
	first := claimTask(t, store, node)
	clock = clock.Add(4 * time.Minute)
	expiresAt, err := store.RenewTaskLease(ctx, node.ID, node.Credential, first.ID, first.Attempt)
	if err != nil || !expiresAt.Equal(clock.Add(taskLeaseDuration)) {
		t.Fatalf("renewed lease = %s, err=%v", expiresAt, err)
	}
	clock = clock.Add(2 * time.Minute)
	if err := store.recoverExpiredTasks(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM deployments WHERE id = ?`, first.ID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("renewed task state = %q, err=%v", state, err)
	}
	clock = expiresAt.Add(time.Second)
	if _, err := store.RenewTaskLease(ctx, node.ID, node.Credential, first.ID, first.Attempt); !errors.Is(err, errStaleTaskLease) {
		t.Fatalf("expired lease was resurrected: %v", err)
	}
	second := claimTask(t, store, node)
	if second.ID != first.ID || second.Attempt != first.Attempt+1 {
		t.Fatalf("expired task retry = %#v, first=%#v", second, first)
	}
	if _, err := store.RenewTaskLease(ctx, node.ID, node.Credential, first.ID, first.Attempt); !errors.Is(err, errStaleTaskLease) {
		t.Fatalf("stale attempt renewed the newer lease: %v", err)
	}
	if _, err := store.RenewTaskLease(ctx, node.ID, node.Credential, second.ID, second.Attempt); err != nil {
		t.Fatalf("current attempt could not renew: %v", err)
	}
}

func TestDeploymentCompletionUsesCapturedServiceAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "address-snapshot", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: config}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.ServiceAddress != "10.0.0.11" {
		t.Fatalf("claimed service address = %q", task.ServiceAddress)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET service_address = '10.0.0.12', lan_address = '10.0.0.12' WHERE agent_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(ApplicationTaskResult{Services: []ApplicationServiceResult{{Name: "api", Protocol: "http", ContainerPort: 8317, HostPort: 8317, Address: "10.0.0.11"}}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var deploymentState, applicationStatus, endpoint string
	if err := store.db.QueryRowContext(ctx, `SELECT d.state, a.status, s.endpoint FROM deployments d JOIN applications a ON a.id = d.application_id JOIN services s ON s.application_id = a.id WHERE d.id = ?`, task.ID).Scan(&deploymentState, &applicationStatus, &endpoint); err != nil {
		t.Fatal(err)
	}
	if deploymentState != "succeeded" || applicationStatus != "running" || endpoint != "10.0.0.11:8317" {
		t.Fatalf("deployment=%q application=%q endpoint=%q", deploymentState, applicationStatus, endpoint)
	}
}

func TestNetworkProfileChangeAndDeploymentCreationRemainConsistent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "concurrent-address-snapshot", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: "10.0.0.31", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "10.0.0.32", Interface: "eth1", Kind: networking.KindLAN},
	}, networking.Profile{ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", EnabledKinds: []string{networking.KindLAN}})

	type deploymentResult struct {
		view DeploymentView
		err  error
	}
	start := make(chan struct{})
	profileResult := make(chan error, 1)
	deploymentResults := make(chan deploymentResult, 1)
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-start
		_, err := store.ConfirmNetworkProfile(ctx, node.ID, networking.Profile{ServiceAddress: "10.0.0.32", LANAddress: "10.0.0.32", EnabledKinds: []string{networking.KindLAN}})
		profileResult <- err
	}()
	go func() {
		ready.Done()
		<-start
		view, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)})
		deploymentResults <- deploymentResult{view: view, err: err}
	}()
	ready.Wait()
	close(start)
	profileErr := <-profileResult
	deployment := <-deploymentResults
	if deployment.err != nil {
		t.Fatal(deployment.err)
	}

	var capturedAddress, confirmedAddress string
	if err := store.db.QueryRowContext(ctx, `SELECT service_address FROM deployments WHERE id = ?`, deployment.view.ID).Scan(&capturedAddress); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id = ?`, node.ID).Scan(&confirmedAddress); err != nil {
		t.Fatal(err)
	}
	if profileErr == nil {
		if capturedAddress != "10.0.0.32" || confirmedAddress != "10.0.0.32" {
			t.Fatalf("successful profile change raced its deployment snapshot: captured=%q confirmed=%q", capturedAddress, confirmedAddress)
		}
		return
	}
	if !strings.Contains(profileErr.Error(), "before changing the private service address") || capturedAddress != "10.0.0.31" || confirmedAddress != "10.0.0.31" {
		t.Fatalf("rejected profile change left inconsistent state: profileErr=%v captured=%q confirmed=%q", profileErr, capturedAddress, confirmedAddress)
	}
}

func TestTaskLongPollWakesWhenACommandIsQueued(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "long-poll", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
	siteID := testSiteID(t, store)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('long-poll-controller', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	result := make(chan *AgentTask, 1)
	errors := make(chan error, 1)
	go func() {
		task, err := store.WaitAndClaimNextTask(ctx, node.ID, node.Credential, 5*time.Second)
		if err != nil {
			errors <- err
			return
		}
		result <- task
	}()
	time.Sleep(50 * time.Millisecond)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	commandID := "application-command-long-poll"
	input, _ := json.Marshal(ThreeXUIClientCommandTask{Action: "list", Inbounds: []ThreeXUIClientInbound{}})
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, 'long-poll-controller', ?, ?, ?, ?, 'pending', ?, ?)`, commandID, node.ID, node.ID, clientCommandKind, input, now, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := store.recordTaskEvent(ctx, tx, commandID, node.ID, "application.command", 1, "queued", "test command queued"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	case task := <-result:
		if task == nil || task.ID != commandID || task.ClientCommand == nil || task.ClientCommand.Action != "list" {
			t.Fatalf("unexpected long-polled task: %#v", task)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("queued task did not wake the Agent long poll")
	}
}

func TestDeploymentLifecyclePreventsDuplicateInstallAndControlsDataDeletion(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "uninstall"}); err == nil {
		t.Fatal("uninstall was accepted before installation")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err == nil || !strings.Contains(err.Error(), "active deployment task") {
		t.Fatalf("parallel install was not rejected: %v", err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.20"}]}`))
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err == nil || !strings.Contains(err.Error(), "use upgrade") {
		t.Fatalf("duplicate install was not rejected: %v", err)
	}
	uninstall, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "uninstall", DeleteData: false})
	if err != nil {
		t.Fatal(err)
	}
	if uninstall.DeleteData {
		t.Fatal("uninstall deleted data without explicit selection")
	}
	completeNextTask(t, store, node, "application.apply", nil)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "upgrade"}); err == nil {
		t.Fatal("upgrade was accepted after uninstall")
	}
}

func TestConfigureReusesInstalledVersionAndEncryptedValues(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.30", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.30", LANAddress: "10.0.0.30", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: initial}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.30"}]}`))
	configured, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "configure", Config: json.RawMessage(`{"timezone":"Asia/Singapore","debug":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if configured.AppVersion != "7.2.128" {
		t.Fatalf("configure changed installed version: %#v", configured)
	}
	task := claimTask(t, store, node)
	var config map[string]any
	var secrets map[string]string
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil {
		t.Fatalf("configure task returned invalid configuration: %#v", task)
	}
	if config["timezone"] != "Asia/Singapore" || config["debug"] != true || secrets["management_key"] != "management-secret" || secrets["api_key"] != "client-secret" {
		t.Fatalf("configure did not merge encrypted prior values: config=%#v secrets=%#v", config, secrets)
	}
}

func TestUpgradeRequiresANewerCatalogVersionAndRejectsDowngrade(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "versioned", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.31", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.31"}]}`)
	completeNextTask(t, store, node, "application.apply", result)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"}); err == nil || !strings.Contains(err.Error(), "already at version") {
		t.Fatalf("same-version upgrade was accepted: %v", err)
	}
	seedCatalogVersion(t, store, "7.2.129")
	upgrade, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"})
	if err != nil || upgrade.AppVersion != "7.2.129" {
		t.Fatalf("newer version was not accepted: %#v err=%v", upgrade, err)
	}
	completeNextTask(t, store, node, "application.apply", result)
	seedCatalogVersion(t, store, "7.2.127")
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"}); err == nil || !strings.Contains(err.Error(), "downgrade is not allowed") {
		t.Fatalf("downgrade was accepted: %v", err)
	}
}

func TestFailedChangeRemainsAnInstalledApplication(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "degraded", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.32", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.32", LANAddress: "10.0.0.32", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.32"}]}`)
	completeNextTask(t, store, node, "application.apply", result)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{"debug":true}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "replacement failed", nil, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	applications, err := store.ListApplications(ctx)
	if err != nil || len(applications) != 1 || applications[0].InstalledVersion != "7.2.128" || applications[0].Status != "failed" {
		t.Fatalf("failed change lost installed state: %#v err=%v", applications, err)
	}
}

func TestInstalledAppCanBeUninstalledAfterCatalogRemoval(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "catalog-removed", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.33", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.33", LANAddress: "10.0.0.33", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.33"}]}`))
	if _, err := store.db.ExecContext(ctx, `UPDATE catalog_sources SET enabled = 0`); err != nil {
		t.Fatal(err)
	}
	uninstall, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "uninstall"})
	if err != nil || uninstall.AppVersion != "7.2.128" {
		t.Fatalf("installed app became unmanageable after catalog removal: %#v err=%v", uninstall, err)
	}
}

func seedCatalogVersion(t *testing.T, store *Store, version string) {
	t.Helper()
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte(`"version": "7.2.128"`), []byte(`"version": "`+version+`"`), 1)
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
}

func TestTaskIDsAreScopedByAgentAndRevision(t *testing.T) {
	if gatewayRouteTaskID("agent-a", 1) == gatewayRouteTaskID("agent-b", 1) || gatewayComponentTaskID("agent-a", 1) == gatewayComponentTaskID("agent-b", 1) || tunnelTaskID("agent-a", 1) == tunnelTaskID("agent-b", 1) {
		t.Fatal("task IDs collide across agents")
	}
	if revision, ok := gatewayTaskRevision(gatewayRouteTaskID("agent-a", 7)); !ok || revision != 7 {
		t.Fatal("gateway task revision was not parsed")
	}
	if revision, ok := tunnelTaskRevision(tunnelTaskID("agent-a", 8)); !ok || revision != 8 {
		t.Fatal("Tunnel task revision was not parsed")
	}
}

func TestThreeXUICredentialsAreReturnedOnceAndRedactedFromLists(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.40", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.40", LANAddress: "10.0.0.40", EnabledKinds: []string{networking.KindLAN}})
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/3x-ui", Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if created.OneTimeCredentials == nil || created.OneTimeCredentials.Username == "" || len(created.OneTimeCredentials.Password) < 20 {
		t.Fatalf("3x-ui did not return strong one-time credentials: %#v", created.OneTimeCredentials)
	}
	task := claimTask(t, store, node)
	var secrets map[string]string
	if json.Unmarshal(task.Secrets, &secrets) != nil || secrets["username"] != created.OneTimeCredentials.Username || secrets["password"] != created.OneTimeCredentials.Password {
		t.Fatalf("Agent task did not receive matching encrypted credentials: %#v", secrets)
	}
	result := json.RawMessage(`{"services":[{"name":"panel","protocol":"http","containerPort":2053,"hostPort":2053,"address":"10.0.0.40"},{"name":"subscription","protocol":"http","containerPort":2096,"hostPort":2096,"address":"10.0.0.40"}],"generatedSecrets":{"api_token":"local-api-token"}}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(listed)
	if bytes.Contains(encoded, []byte(created.OneTimeCredentials.Password)) || bytes.Contains(encoded, []byte("local-api-token")) || listed[0].OneTimeCredentials != nil {
		t.Fatalf("deployment list leaked one-time credentials: %s", encoded)
	}
}

func TestThreeXUIDeploymentsAndDataPlaneCommandsAreMutuallyExclusive(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "serialized-controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.42", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.42", LANAddress: "10.0.0.42", EnabledKinds: []string{networking.KindLAN}})
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	result := json.RawMessage(`{"services":[{"name":"panel","protocol":"http","containerPort":2053,"hostPort":2053,"address":"10.0.0.42"},{"name":"subscription","protocol":"http","containerPort":2096,"hostPort":2096,"address":"10.0.0.42"}],"generatedSecrets":{"api_token":"local-api-token"}}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('active-reality-command', ?, ?, ?, ?, '{}', 'running', ?, ?)`, created.ApplicationID, node.ID, node.ID, realityCommandKind, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Operation: "uninstall"}); err == nil || !strings.Contains(err.Error(), "data-plane operation is in progress") {
		t.Fatalf("3x-ui uninstall raced an active data-plane command: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM application_commands WHERE id = 'active-reality-command'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, error, created_at, updated_at)
		VALUES('failed-client-command', ?, ?, ?, ?, '{}', 'failed', 'previous failure', ?, ?)`, created.ApplicationID, node.ID, node.ID, clientCommandKind, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Operation: "configure", Config: json.RawMessage(`{"enable_fail2ban":false}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state = 'pending', error = '' WHERE id = 'failed-client-command'`); err == nil || !strings.Contains(err.Error(), "3x-ui deployment is in progress") {
		t.Fatalf("retried data-plane command raced an active 3x-ui deployment: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('racing-client-command', ?, ?, ?, ?, '{}', 'pending', ?, ?)`, created.ApplicationID, node.ID, node.ID, clientCommandKind, now, now); err == nil || !strings.Contains(err.Error(), "3x-ui deployment is in progress") {
		t.Fatalf("data-plane command raced an active 3x-ui deployment: %v", err)
	}
}

func TestIncompleteEndpointObservationPreservesLastSnapshot(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.41", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.41", LANAddress: "10.0.0.41", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	result := json.RawMessage(`{"services":[{"name":"panel","protocol":"http","containerPort":2053,"hostPort":2053,"address":"10.0.0.41"},{"name":"subscription","protocol":"http","containerPort":2096,"hostPort":2096,"address":"10.0.0.41"}],"generatedSecrets":{"api_token":"local-api-token"}}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, ApplicationEndpointsObserved: true, ApplicationEndpoints: []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-7", Protocol: "tcp", AppProtocol: "vless/tcp", Listen: "0.0.0.0", Port: 443, Enabled: true}}}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observedStatus := ""
	for _, service := range services {
		if service.Name == "inbound-7" {
			observedStatus = service.Status
		}
	}
	if observedStatus != "ready" {
		t.Fatalf("initial endpoint observation was not stored: %#v", services)
	}
	heartbeat.ApplicationEndpoints = nil
	heartbeat.ApplicationEndpointsObserved = false
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err = store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if service.Name == "inbound-7" && service.Status != "ready" {
			t.Fatalf("incomplete observation stopped the last known endpoint: %#v", service)
		}
	}
	heartbeat.ApplicationEndpointsObserved = true
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err = store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if service.Name == "inbound-7" && service.Status != "stopped" {
			t.Fatalf("complete empty observation did not stop the disappeared endpoint: %#v", service)
		}
	}
}
