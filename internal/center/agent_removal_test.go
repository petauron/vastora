package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func expireRemovalNode(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, s.now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

func removalCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRemoveOfflineAgentCleansInstallationsAndPreservesOtherNodes(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "Edge Node B", "10.0.0.80")
	other := enrollAccessTestNode(t, s, "other", "10.0.0.81")
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO site_gateways(site_id,agent_id,created_at) SELECT site_id,id,'' FROM agents WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	installation, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	otherInstall, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: other.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	// An expired node may be stuck in an uninstall or an uncertain update.
	if _, err = s.db.Exec(`UPDATE deployments SET operation='uninstall',state='running',reconciliation_required=1,attempt=1 WHERE id=?`, installation.ID); err != nil {
		t.Fatal(err)
	}
	// Seed a dependent history row and then force a new physical connection.
	// A startup-only PRAGMA silently loses both the cascade and RESTRICT guards.
	if _, err := s.db.Exec(`INSERT INTO task_events(id,task_id,agent_id,kind,event,created_at) VALUES('retired-event','retired-task',?,'test','queued','')`, node.ID); err != nil {
		t.Fatal(err)
	}
	replaceStoreConnection(t, s)
	expireRemovalNode(t, s, node.ID)
	if err = s.StartAgentRemoval(ctx, node.ID, " Edge Node B "); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test"}); err == nil {
		t.Fatal("retired credential accepted")
	}
	if _, err = s.WaitAndClaimNextTask(ctx, node.ID, node.Credential, 0); err == nil {
		t.Fatal("retired node claimed work")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM deployments WHERE id=? AND state='failed' AND reconciliation_required=0`, installation.ID) != 1 {
		t.Fatal("offline work was not abandoned")
	}
	if err = s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agents", "applications", "deployments", "site_gateways", "agent_network_profiles", "agent_removals"} {
		column := "agent_id"
		if table == "agents" {
			column = "id"
		}
		if table == "applications" {
			column = "node_id"
		}
		if removalCount(t, s, `SELECT COUNT(*) FROM `+table+` WHERE `+column+`=?`, node.ID) != 0 {
			t.Fatalf("%s records remained", table)
		}
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM task_events WHERE id='retired-event'`) != 0 {
		t.Fatal("orphan task history remained after node removal")
	}
	if err := verifyMigratedSchema(ctx, s.db, centerSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM deployments WHERE id=? AND state='pending'`, otherInstall.ID) != 1 {
		t.Fatal("other installation changed")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM site_gateways WHERE agent_id=?`, other.ID) != 1 {
		t.Fatal("other gateway changed")
	}
	if err = s.authenticateAgent(ctx, other.ID, other.Credential); err != nil {
		t.Fatal(err)
	}
	if err = s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal("completed cleanup is not idempotent", err)
	}
}

func TestRemoveOfflineAgentRejectsOnlineWrongNameAndRollsBackIntent(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "expired", "10.0.0.82")
	if err := s.StartAgentRemoval(ctx, node.ID, "expired"); !errors.Is(err, errNodeRemovalOnline) {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, node.ID)
	if err := s.StartAgentRemoval(ctx, node.ID, "wrong"); !errors.Is(err, errNodeRemovalName) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_removal_intent BEFORE INSERT ON agent_removals BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.StartAgentRemoval(ctx, node.ID, "expired"); err == nil {
		t.Fatal("intent failure ignored")
	}
	if err := s.authenticateAgent(ctx, node.ID, node.Credential); err != nil {
		t.Fatal("intent failure revoked node", err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM agent_removals`) != 0 {
		t.Fatal("partial removal intent")
	}
}

func TestRemoveOfflineAgentDeletionFailureRetainsRetryState(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "expired", "10.0.0.83")
	d, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, node.ID)
	if err = s.StartAgentRemoval(ctx, node.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_removal_delete BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.resumeAgentRemovals(ctx); err == nil {
		t.Fatal("delete failure ignored")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id=?`, d.ApplicationID) != 1 {
		t.Fatal("failed transaction deleted installation")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM agent_removals WHERE agent_id=? AND state='failed'`, node.ID) != 1 {
		t.Fatal("retry state lost")
	}
	if err = s.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("ordinary delete bypassed removal")
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_removal_delete`); err != nil {
		t.Fatal(err)
	}
	if err = s.StartAgentRemoval(ctx, node.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if err = s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM agents WHERE id=?`, node.ID) != 0 {
		t.Fatal("retry did not finish")
	}
}

func TestRemoveOfflineAgentKeepsSharedSecret(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "expired", "10.0.0.84")
	d, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.putSecret(ctx, tx, []byte("shared-test"), "test")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.putSecret(ctx, tx, []byte("owned-test"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		q string
		a []any
	}{
		{`UPDATE deployments SET secret_id=? WHERE id=?`, []any{shared, d.ID}},
		{`INSERT INTO application_secrets(application_id,secret_id,updated_at) VALUES(?,?,'') ON CONFLICT(application_id) DO UPDATE SET secret_id=excluded.secret_id`, []any{d.ApplicationID, owned}},
		{`CREATE TABLE removal_test_shared_owner(secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL)`, nil},
		{`INSERT INTO removal_test_shared_owner(secret_id) VALUES(?)`, []any{shared}},
	} {
		if _, err = tx.ExecContext(ctx, statement.q, statement.a...); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, node.ID)
	if err = s.StartAgentRemoval(ctx, node.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if err = s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM secrets WHERE id=?`, owned) != 0 {
		t.Fatal("owned secret remained")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM removal_test_shared_owner WHERE secret_id=?`, shared) != 1 {
		t.Fatal("shared SET NULL secret was deleted")
	}
}

func TestRemoveOfflineAgentRetainsApplicationInventoryWithoutLegacyCleanup(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	master := enrollAccessTestNode(t, s, "controller", "10.0.0.90")
	worker := enrollOrchestrationNode(t, s, "expired-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.91", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.91", HeadscaleAddress: "100.64.0.91", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := s.db.Exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, worker.ID); err != nil {
		t.Fatal(err)
	}
	// The retired catalog cannot install 3x-ui. Seed the persisted topology
	// that an existing installation still needs to clean up during migration.
	const masterApplicationID = "removal-legacy-controller"
	const workerApplicationID = "removal-legacy-worker"
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at)
		VALUES(?, 'Legacy controller', ?, ?, ?, 'running', 'docker', 'master', ?, ?),
		(?, 'Legacy worker', ?, ?, ?, 'running', 'docker', 'worker', ?, ?)`,
		masterApplicationID, master.ID, testSiteID(t, s), threeXUIAppKey, stamp, stamp,
		workerApplicationID, worker.ID, testSiteID(t, s), threeXUIAppKey, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	selectTestThreeXUIController(t, s, masterApplicationID)
	if _, err = s.db.Exec(`INSERT INTO three_x_ui_nodes(worker_application_id,master_application_id,remote_node_id,status,created_at,updated_at)
		VALUES(?,?,7,'ready',?,?)`, workerApplicationID, masterApplicationID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, master.ID)
	if err = s.StartAgentRemoval(ctx, master.ID, "controller"); !errors.Is(err, errNodeRemovalShared) {
		t.Fatal("shared controller not protected", err)
	}
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), master.ID); err != nil {
		t.Fatal(err)
	}
	privateDeletes := 0
	serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			// Current Headscale response: tagged-devices owns the node, and tags
			// replaces the removed forcedTags/validTags fields.
			_, _ = w.Write([]byte(`{"nodes":[{"id":"10","nodeKey":"nodekey:worker-key","ipAddresses":["100.64.0.91"],"user":{"name":"tagged-devices"},"tags":["tag:vastora-agent","tag:vastora-gateway"]}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/node/10":
			privateDeletes++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected private cleanup %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	expireRemovalNode(t, s, worker.ID)
	if err = s.StartAgentRemoval(ctx, worker.ID, "expired-worker"); !errors.Is(err, errNodeRemovalRetainedData) {
		t.Fatal("active application data did not fence permanent removal", err)
	}
	if privateDeletes != 0 || removalCount(t, s, `SELECT COUNT(*) FROM agent_removals WHERE agent_id=?`, worker.ID) != 0 {
		t.Fatal("blocked removal touched private identity or durable intent")
	}
	if err = s.authenticateAgent(ctx, worker.ID, worker.Credential); err != nil {
		t.Fatal("blocked removal revoked the retained node", err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id IN (?,?)`, workerApplicationID, masterApplicationID) != 2 ||
		removalCount(t, s, `SELECT COUNT(*) FROM three_x_ui_nodes WHERE worker_application_id=? AND remote_node_id=7 AND status='ready'`, workerApplicationID) != 1 ||
		removalCount(t, s, `SELECT COUNT(*) FROM application_commands WHERE application_id=?`, workerApplicationID) != 0 {
		t.Fatal("blocked removal changed topology or queued retired proxy work")
	}
	// The owner's eventual retirement is a separate operation. An upgrade must
	// neither guess completion nor purge the inventory to make removal succeed.
}

func TestRemoveOfflineMeridianAgentPreservesUnretiredDataAfterLegacyCleanup(t *testing.T) {
	s := openMeridianSharedEndpointSnapshotFixture(t)
	defer s.Close()
	ctx := context.Background()
	controller := enrollAccessTestNode(t, s, "Glivela subscription host", "10.0.0.92")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	routeSecretID, err := s.putSecret(ctx, tx, []byte("22222222-2222-4222-8222-222222222222"), meridianCredentialSecretContext("removal-route-credential"))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var workerID string
	if err := s.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		q string
		a []any
	}{
		{`UPDATE agents SET tailscale_ownership='' WHERE id=?`, []any{workerID}},
		{`UPDATE meridian_endpoints SET legacy_retired=1 WHERE id=?`, []any{sharedSnapshotEndpointID}},
		{`INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at) VALUES('removal-route-credential',?,?,'route','removal-route-credential',? ,?,?,1,?,?)`, []any{sharedSnapshotAccountA, sharedSnapshotEndpointID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", routeSecretID, controller.ID, stamp, stamp}},
		{`INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at) VALUES('removal-route',?,?,?,?,'removal-route-credential',1,'ready',?,?)`, []any{sharedSnapshotAccountA, sharedSnapshotEndpointID, controller.ID, sharedSnapshotAccountA + "-native", stamp, stamp}},
		{`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('removal-meridian-controller','Meridian',?,? ,?,'running','docker','',?,?)`, []any{controller.ID, testSiteID(t, s), meridianAppKey, stamp, stamp}},
		{`UPDATE meridian_cutover SET state='complete',subscription_authority='meridian',legacy_controller_application_id='removal-meridian-controller' WHERE id=1`, nil},
		{`INSERT INTO three_x_ui_nodes(worker_application_id,master_application_id,remote_node_id,status,created_at,updated_at) VALUES('snapshot-shared-app','removal-meridian-controller',8,'failed',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,error,created_at,updated_at) VALUES('removal-obsolete-command','snapshot-shared-app',?,?,?,'{"action":"remove","workerApplicationId":"snapshot-shared-app","remoteNodeId":8}','failed',1,'agent: official 3x-ui is not installed',?,?)`, []any{controller.ID, controller.ID, nodeCommandKind, stamp, stamp}},
	} {
		if _, err := s.db.Exec(statement.q, statement.a...); err != nil {
			t.Fatal(err)
		}
	}
	expireRemovalNode(t, s, workerID)
	var name string
	if err := s.db.QueryRow(`SELECT name FROM agents WHERE id=?`, workerID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := s.StartAgentRemoval(ctx, workerID, name); !errors.Is(err, errNodeRemovalRetainedData) {
		t.Fatal("legacy retirement incorrectly authorized deletion of live Glivela data", err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM agent_removals WHERE agent_id=?`, workerID) != 0 ||
		removalCount(t, s, `SELECT COUNT(*) FROM agents WHERE id=? AND credential_revoked_at=''`, workerID) != 1 {
		t.Fatal("retained-data refusal changed node identity or removal intent")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM application_commands WHERE id='removal-obsolete-command' AND state='failed' AND attempt=1`) != 1 {
		t.Fatal("obsolete cleanup was replayed or reported successful")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM meridian_endpoints WHERE id=? AND legacy_retired=1 AND status<>'retired'`, sharedSnapshotEndpointID) != 1 ||
		removalCount(t, s, `SELECT COUNT(*) FROM meridian_credentials WHERE id='removal-route-credential' AND enabled=1 AND protocol_secret_id=?`, routeSecretID) != 1 ||
		removalCount(t, s, `SELECT COUNT(*) FROM meridian_route_grants WHERE id='removal-route' AND enabled=1 AND status='ready'`) != 1 {
		t.Fatal("retained route, credentials or endpoint were modified")
	}
	if err := s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM meridian_accounts`) != 2 ||
		removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id='removal-meridian-controller' AND status='running'`) != 1 {
		t.Fatal("shared accounts or subscription host changed")
	}
	if err := verifyMigratedSchema(ctx, s.db, centerSchemaVersion); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOfflineAgentProtectedGatewayReturnsUsefulError(t *testing.T) {
	if got := errorCode(http.StatusBadRequest, "center: uninstall active applications before disabling this node"); got != "node_disable_in_use" {
		t.Fatal(got)
	}
	for _, err := range []error{errNodeRemovalOnline, errNodeRemovalName, errNodeRemovalShared} {
		if got := errorCode(http.StatusBadRequest, err.Error()); got == "invalid_request" {
			t.Fatal("specific removal error lost", err)
		}
	}
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "center-host", "10.0.0.85")
	if _, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)`, setupGatewayBindingSetting, `{"bindAddress":"10.0.0.85","publicAddress":"203.0.113.85"}`); err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, node.ID)
	if err := s.StartAgentRemoval(ctx, node.ID, "center-host"); !errors.Is(err, errNodeRemovalShared) {
		t.Fatal(err)
	}
	if err := s.authenticateAgent(ctx, node.ID, node.Credential); err != nil {
		t.Fatal("protected host was revoked", err)
	}
}

func TestRemoveOfflineAgentPrivateIdentityRetryDoesNotDeleteReplacement(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "private-expired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.86", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.86", HeadscaleAddress: "100.64.0.86", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := s.db.Exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, node.ID)
	deleted := []string{}
	oldExists := true
	serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			key, id := "old-key", "10"
			if !oldExists {
				key, id = "replacement-key", "11"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{map[string]any{"id": id, "nodeKey": key, "ipAddresses": []string{"100.64.0.86"}, "user": map[string]string{"name": "tagged-devices"}, "tags": []string{"tag:vastora-agent"}}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/node/10":
			deleted = append(deleted, r.URL.Path)
			oldExists = false
			http.Error(w, "response lost", http.StatusBadGateway)
		default:
			t.Errorf("unexpected private cleanup %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	if err := s.StartAgentRemoval(ctx, node.ID, "private-expired"); err != nil {
		t.Fatal(err)
	}
	if err := s.resumeAgentRemovals(ctx); err == nil {
		t.Fatal("lost response ignored")
	}
	if err := s.StartAgentRemoval(ctx, node.ID, "private-expired"); err != nil {
		t.Fatal(err)
	}
	if err := s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 {
		t.Fatalf("replacement private node touched: %v", deleted)
	}
}

func TestRemoveOfflineAgentDeletesOnlyItsManagedTunnel(t *testing.T) {
	remote := &cloudflareTunnelOperationAPI{}
	s, id := openCloudflareTunnelOperationTestStore(t, remote)
	ctx := context.Background()
	if err := s.ensureCloudflareTunnel(ctx, id); err != nil {
		t.Fatal(err)
	}
	expireRemovalNode(t, s, id)
	if err := s.StartAgentRemoval(ctx, id, "Tunnel node"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureCloudflareTunnel(ctx, id); err == nil {
		t.Fatal("retired node reused a tunnel")
	}
	if err := s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.deleteCalls != 1 || remote.remote {
		t.Fatalf("tunnel not removed once: %+v", remote)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM agents WHERE id=?`, id) != 0 {
		t.Fatal("node remained")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM network_integrations WHERE kind='cloudflare'`) != 1 {
		t.Fatal("shared integration removed")
	}
}

func TestRemoveOfflineAgentCancelsCollectorEnrollmentOnSurvivingService(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollAccessTestNode(t, s, "expired", "10.0.0.95")
	service := enrollAccessTestNode(t, s, "monitor", "10.0.0.96")
	for _, statement := range []struct {
		q string
		a []any
	}{
		{`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) SELECT 'pulse-owner','Pulse',id,site_id,'vastora-official/pulse','running','','' FROM agents WHERE id=?`, []any{service.ID}},
		{`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES('pulse-pending','pulse-owner',?,?,'pulse.enrollment.create','{}','running','','')`, []any{service.ID, node.ID}},
		{`INSERT INTO task_events(id,task_id,agent_id,kind,revision,event,message,created_at) VALUES('pulse-event','pulse-pending',?,'application.command',1,'queued','', '')`, []any{service.ID}},
	} {
		if _, err := s.db.Exec(statement.q, statement.a...); err != nil {
			t.Fatal(err)
		}
	}
	expireRemovalNode(t, s, node.ID)
	if err := s.StartAgentRemoval(ctx, node.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM application_commands WHERE id='pulse-pending' AND state='failed'`) != 1 {
		t.Fatal("collector enrollment not cancelled")
	}
	if err := s.resumeAgentRemovals(ctx); err != nil {
		t.Fatal(err)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM application_commands WHERE id='pulse-pending'`) != 0 || removalCount(t, s, `SELECT COUNT(*) FROM task_events WHERE id='pulse-event'`) != 0 {
		t.Fatal("collector history remained on shared service")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id='pulse-owner' AND status='running'`) != 1 {
		t.Fatal("shared monitor changed")
	}
}
