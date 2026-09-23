package center

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

func TestFreshMeridianAuthorityIsCompleteOnlyAfterExpectedInventoryMatches(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	view, err := store.MeridianCutover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "not_required" || view.SubscriptionAuthority != "meridian" || view.Complete {
		t.Fatalf("fresh cutover=%#v", view)
	}
	if _, err := store.db.Exec(`UPDATE meridian_cutover SET state='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	view, err = store.MeridianCutover(context.Background())
	if err != nil || !view.Complete {
		t.Fatalf("completed cutover=%#v err=%v", view, err)
	}
}

func TestMeridianCutoverRequiresExplicitExecutionRecovery(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "cutover-execution-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.54", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.54", LANAddress: "10.0.0.54", EnabledKinds: []string{networking.KindLAN}})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "cutover-execution-fence-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','master',?,?)`, applicationID, "3x-ui", node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at)
		VALUES('cutover-unresolved-execution',?,'cutover-unresolved-task','landing.proxy.apply',1,'cutover-session','cutover-digest',X'00','failed','reported','failed before cutover',?,?,?)`, node.ID, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	check := func() error {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return ensureMeridianCutoverIdle(ctx, tx, applicationID)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "explicitly recover unresolved node executions") {
		t.Fatalf("unresolved execution did not fence cutover: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_executions SET disposition='abandon',disposition_note='verified stopped',disposed_at=?,updated_at=? WHERE id='cutover-unresolved-execution'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("resolved execution still fenced cutover: %v", err)
	}
}

func TestMeridianCutoverClaimsOnlyItsExistingControllerBackup(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "cutover-backup-claim", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.55", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.55", LANAddress: "10.0.0.55", EnabledKinds: []string{networking.KindLAN}})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "cutover-backup-claim-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','master',?,?)`, applicationID, "3x-ui", node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
		VALUES('cutover-backup-claim',?,?,?,'3xui.controller.manage',?,'pending',?,?)`, applicationID, node.ID, node.ID, []byte(`{"action":"backup","applicationId":"cutover-backup-claim-application","backupRevision":7}`), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='backup',subscription_authority='legacy',legacy_controller_application_id=?,backup_revision=7,updated_at=? WHERE id=1`, applicationID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=attempt+1 WHERE id='cutover-backup-claim'`); err != nil {
		t.Fatalf("authorized cutover backup was fenced: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state='running' WHERE id='cutover-backup-claim'`); err != nil {
		t.Fatalf("authorized backup result confirmation was fenced: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state='succeeded' WHERE id='cutover-backup-claim'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state='running' WHERE id='cutover-backup-claim'`); err == nil || !strings.Contains(err.Error(), "Meridian authority cutover") {
		t.Fatalf("completed backup replay crossed cutover fence: %v", err)
	}
}

func TestMeridianCutoverFencesNewLegacyMutations(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "cutover-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.53", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.53", LANAddress: "10.0.0.53", EnabledKinds: []string{networking.KindLAN}})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "cutover-fence-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','master',?,?)`, applicationID, "3x-ui", node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
		VALUES('existing-legacy-command',?,?,?,?,?,'failed',?,?)`, applicationID, node.ID, node.ID, clientCommandKind, []byte(`{}`), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,application_id,created_at,updated_at)
		VALUES('existing-legacy-deployment',?,'vastora-official/3x-ui','0.1.0','{}','{}','upgrade','failed',?,?,?)`, node.ID, applicationID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='backup',subscription_authority='legacy',legacy_controller_application_id=?,backup_revision=1,updated_at=? WHERE id=1`, applicationID, stamp); err != nil {
		t.Fatal(err)
	}
	serverState := landing.ServerState{NodeID: node.ID, Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.53"}}
	proxyState := landing.DesiredState{NodeID: node.ID, Revision: 1}
	serverJSON, err := json.Marshal(serverState)
	if err != nil {
		t.Fatal(err)
	}
	proxyJSON, err := json.Marshal(proxyState)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,desired_json,status,updated_at) VALUES(?,1,?,'pending',?)`, node.ID, serverJSON, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at) VALUES(?,?,?,1,'100.64.0.54',1,?,'pending',?)`, node.ID, applicationID, node.ID, proxyJSON, stamp); err != nil {
		t.Fatal(err)
	}
	landingTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileGlobalLandingPool(ctx, landingTx, true); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing reconciliation did not yield to Meridian: %v", err)
	}
	if err := store.queueLandingServer(ctx, landingTx, node.ID, serverState.Plan); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing server queue did not yield to Meridian: %v", err)
	}
	if err := store.queueLandingClientCommand(ctx, landingTx, landingGrantRecord{}, "prepare"); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing client queue did not yield to Meridian: %v", err)
	}
	if err := store.queueNextLandingClientCommand(ctx, landingTx, node.ID); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing client refill did not yield to Meridian: %v", err)
	}
	if err := store.queueClientLandingRoutes(ctx, landingTx, applicationID); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing route queue did not yield to Meridian: %v", err)
	}
	if err := store.refreshClientLandingSources(ctx, landingTx, node.ID); err != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing source refresh did not yield to Meridian: %v", err)
	}
	serverTask, err := store.claimLandingServerTask(ctx, landingTx, node.ID)
	if err != nil || serverTask != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing server task crossed cutover fence: task=%#v err=%v", serverTask, err)
	}
	proxyTask, err := store.claimLandingProxyTask(ctx, landingTx, node.ID)
	if err != nil || proxyTask != nil {
		_ = landingTx.Rollback()
		t.Fatalf("legacy landing proxy task crossed cutover fence: task=%#v err=%v", proxyTask, err)
	}
	if err := landingTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var serverAttempt, proxyAttempt int
	if err := store.db.QueryRowContext(ctx, `SELECT attempt FROM landing_server_states WHERE node_id=?`, node.ID).Scan(&serverAttempt); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT attempt FROM landing_proxy_states WHERE node_id=?`, node.ID).Scan(&proxyAttempt); err != nil {
		t.Fatal(err)
	}
	if serverAttempt != 0 || proxyAttempt != 0 {
		t.Fatalf("legacy landing attempts changed during cutover: server=%d proxy=%d", serverAttempt, proxyAttempt)
	}
	if err := store.SelectLanding(ctx, LandingSelection{}); !errors.Is(err, errMeridianOwnsLegacyLanding) {
		t.Fatalf("landing selection crossed cutover fence: %v", err)
	}
	if err := store.ConfigureLandingProxy(ctx, applicationID, LandingProxyInput{Revision: 1}); !errors.Is(err, errMeridianOwnsLegacyLanding) {
		t.Fatalf("landing proxy mutation crossed cutover fence: %v", err)
	}
	if _, err := store.ConfigureClientLanding(ctx, LandingClientGrantInput{ParentID: "parent", ServiceID: "service", LandingNodeID: node.ID, Mode: landing.FixedMode, Enabled: true, ConfirmSessionReset: true}); !errors.Is(err, errMeridianOwnsLegacyLanding) {
		t.Fatalf("landing client mutation crossed cutover fence: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueScheduledThreeXUIBackup(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.resumeThreeXUIControllerConvergence(ctx); err != nil {
		t.Fatalf("legacy convergence did not yield to Meridian: %v", err)
	}
	var backgroundLegacyCommands int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE kind GLOB '3xui.*' AND (state IN ('pending','running') OR reconciliation_required=1)`).Scan(&backgroundLegacyCommands); err != nil || backgroundLegacyCommands != 0 {
		t.Fatalf("legacy background work crossed cutover fence: count=%d err=%v", backgroundLegacyCommands, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
		VALUES('late-legacy-command',?,?,?,?,?,'pending',?,?)`, applicationID, node.ID, node.ID, clientCommandKind, []byte(`{}`), stamp, stamp); err == nil || !strings.Contains(err.Error(), "Meridian authority cutover") {
		t.Fatalf("legacy command crossed cutover fence: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,application_id,created_at,updated_at)
		VALUES('late-legacy-deployment',?,'vastora-official/3x-ui','0.1.0','{}','{}','upgrade','pending',?,?,?)`, node.ID, applicationID, stamp, stamp); err == nil || !strings.Contains(err.Error(), "Meridian authority cutover") {
		t.Fatalf("legacy deployment crossed cutover fence: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET state='pending' WHERE id='existing-legacy-command'`); err == nil || !strings.Contains(err.Error(), "Meridian authority cutover") {
		t.Fatalf("legacy command retry crossed cutover fence: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state='pending' WHERE id='existing-legacy-deployment'`); err == nil || !strings.Contains(err.Error(), "Meridian authority cutover") {
		t.Fatalf("legacy deployment retry crossed cutover fence: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
		VALUES('authorized-meridian-export',?,?,?,?,?,'pending',?,?)`, applicationID, node.ID, node.ID, "meridian.legacy.export", []byte(`{"applicationId":"cutover-fence-application"}`), stamp, stamp); err != nil {
		t.Fatalf("authorized Meridian command was fenced: %v", err)
	}
}

func TestMeridianCutoverKeepsLegacyListenerAliasesUntilRuntimeReceipt(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	type target struct {
		name, applicationID, serviceID, endpointID, publicationID, publicAddress, serviceAddress, expectedAlias string
	}
	targets := []target{
		{name: "cutover-controller", applicationID: "cutover-controller-app", serviceID: "cutover-controller-service", endpointID: "cutover-controller-endpoint", publicationID: "cutover-controller-publication", publicAddress: "203.0.113.51", serviceAddress: "10.0.0.51", expectedAlias: dockerruntime.ThreeXUIAlias},
		{name: "cutover-worker", applicationID: "cutover-worker-app", serviceID: "cutover-worker-service", endpointID: "cutover-worker-endpoint", publicationID: "cutover-worker-publication", publicAddress: "203.0.113.52", serviceAddress: "10.0.0.52", expectedAlias: dockerruntime.LegacyXrayAlias},
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	nodeIDs := map[string]string{}
	for _, value := range targets {
		node := enrollOrchestrationNode(t, store, value.name, NodeCapabilities{Docker: true}, []networking.Candidate{
			{Address: value.serviceAddress, Interface: "eth0", Kind: networking.KindLAN},
			{Address: value.publicAddress, Interface: "eth0", Kind: networking.KindPublic},
		}, networking.Profile{ServiceAddress: value.serviceAddress, LANAddress: value.serviceAddress, PublicAddress: value.publicAddress, EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
		nodeIDs[value.applicationID] = node.ID
		if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
			VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, value.applicationID, "Meridian", node.ID, siteID, meridianAppKey, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,observed_listen,status,created_at,updated_at)
			VALUES(?,?,?,?,?,'tcp',443,443,?,'observed',?,'0.0.0.0','pending',?,?)`, value.serviceID, value.applicationID, siteID, "inbound-"+value.name, value.name, value.serviceAddress+":443", meridianEntryProtocol, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		secretID, err := store.putSecret(ctx, tx, []byte("test-private-key-"+value.name), meridianEndpointSecretContext(value.endpointID))
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
			VALUES(?,?,?,?,443,?,443,'www.example.com:443','203.0.113.80','["www.example.com"]',?,'test-public-key','["abcd"]','chrome',1,0,0,'pending',?,?)`, value.endpointID, value.applicationID, value.serviceID, "meridian-"+value.name, value.name+".example.test", secretID, stamp, stamp); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,desired_revision,applied_revision,status,created_at,updated_at)
			VALUES(?,?,'public_shared_443','application_node',?,?,?,'manual',1,1,'ready',?,?)`, value.publicationID, value.serviceID, node.ID, value.name+".example.test", "www.example.com", stamp, stamp); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='project',subscription_authority='meridian',legacy_controller_application_id=?,expected_endpoints=2,updated_at=? WHERE id=1`, targets[0].applicationID, stamp); err != nil {
		t.Fatal(err)
	}
	for _, value := range targets {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.desiredNodeListenerState(ctx, tx, nodeIDs[value.applicationID], 1)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		projection, err := store.buildMeridianRuntimeTask(ctx, tx, value.endpointID, "")
		_ = tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Listener.Routes) != 1 || len(state.Listener.Routes[0].Upstreams) != 1 || state.Listener.Routes[0].Upstreams[0].Address != value.expectedAlias {
			t.Fatalf("%s pre-receipt listener=%#v", value.name, state.Listener)
		}
		if !projection.task.PreserveLegacyAliases {
			t.Fatalf("%s project-state runtime did not preserve the live legacy aliases", value.name)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready',updated_at=?`, stamp); err != nil {
		t.Fatal(err)
	}
	for _, value := range targets {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.desiredNodeListenerState(ctx, tx, nodeIDs[value.applicationID], 2)
		_ = tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Listener.Routes) != 1 || len(state.Listener.Routes[0].Upstreams) != 1 || state.Listener.Routes[0].Upstreams[0].Address != dockerruntime.MeridianAlias {
			t.Fatalf("%s post-receipt listener=%#v", value.name, state.Listener)
		}
	}
}

func TestMeridianRuntimeProjectionAllowsEndpointWithoutAccounts(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := enrollOrchestrationNode(t, store, "empty-meridian-entry", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.41", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.41", HeadscaleAddress: "100.64.0.41", EnabledKinds: []string{networking.KindHeadscale}})
	siteID := testSiteID(t, store)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	const (
		applicationID = "empty-meridian-application"
		serviceID     = "empty-meridian-service"
		endpointID    = "empty-meridian-endpoint"
	)
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Empty Meridian entry", node.ID, siteID, meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES(?,?,?,?,?,'US','tcp',443,443,'100.64.0.41:443','observed',?,0,'0.0.0','pending',?,?)`, serviceID, applicationID, siteID, "inbound-1", "🇺🇸 United States｜Empty", meridianEntryProtocol, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	privateKeySecretID, err := store.putSecret(ctx, tx, []byte("test-private-key"), meridianEndpointSecretContext(endpointID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,status,created_at,updated_at)
		VALUES(?,?,?,?,443,'entry.example.test',443,'www.example.com:443','203.0.113.41','["www.example.com"]',?,'test-public-key','["abcd"]','chrome','pending',?,?)`, endpointID, applicationID, serviceID, "meridian-empty", privateKeySecretID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	projection, err := store.buildMeridianRuntimeTask(ctx, tx, endpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if projection.agentID != node.ID || len(projection.materials) != 0 || projection.task.Validate() != nil || !strings.Contains(string(projection.task.Desired.Config), `"clients":[]`) {
		t.Fatalf("empty-account projection=%#v", projection)
	}
}

func TestCreateMeridianEndpointQueuesEmptyRuntimeBeforeFirstAccount(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	profile := networking.Profile{
		ServiceAddress: "10.0.0.42", LANAddress: "10.0.0.42", PublicAddress: "203.0.113.42",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	}
	node := enrollOrchestrationNode(t, store, "empty-first-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "empty-first-meridian-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Empty-first Meridian entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	input := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{
		ApplicationID: applicationID,
		TargetHost:    "www.example.com",
		ServerName:    "www.example.com",
	})
	endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{
		ApplicationID:  applicationID,
		AdvertiseHost:  "entry.example.test",
		Name:           "Empty first",
		RegionCode:     "US",
		VerificationID: input.VerificationID,
		TargetIP:       input.TargetIP,
		TargetHost:     input.TargetHost,
		ServerName:     input.ServerName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Status != "pending" {
		t.Fatalf("empty endpoint status=%q", endpoint.Status)
	}
	var credentialCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_credentials WHERE endpoint_id=?`, endpoint.ID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 0 {
		t.Fatalf("empty endpoint fabricated %d credentials", credentialCount)
	}
	var publicationHostname, publicationSNI, publicationStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT hostname,sni_hostname,status FROM publications WHERE service_id=? AND kind='public_shared_443'`, endpoint.ServiceID).Scan(&publicationHostname, &publicationSNI, &publicationStatus); err != nil {
		t.Fatal(err)
	}
	if publicationHostname != endpoint.AdvertiseHost || publicationSNI != input.ServerName || publicationStatus != "pending" || net.ParseIP(publicationHostname) != nil {
		t.Fatalf("new Meridian publication host=%q sni=%q status=%q endpoint=%#v", publicationHostname, publicationSNI, publicationStatus, endpoint)
	}
}

func TestRecoverMeridianEndpointReleasesOnlyMatchingFenceAndQueuesCenterAuthority(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	profile := networking.Profile{ServiceAddress: "10.0.0.44", LANAddress: "10.0.0.44", PublicAddress: "203.0.113.44", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
	node := enrollOrchestrationNode(t, store, "recover-meridian-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "recover-meridian-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Recover Meridian entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	verified := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
	endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: "entry.example.test", Name: "Recover entry", RegionCode: "US", VerificationID: verified.VerificationID, TargetIP: verified.TargetIP, TargetHost: verified.TargetHost, ServerName: verified.ServerName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET status='failed',last_error='uncertain pending state' WHERE id=?`, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	const failedCommandID = "recover-meridian-failed-command"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,reconciliation_required,attempt,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,'failed',1,1,'uncertain pending state',?,?)`, failedCommandID, applicationID, testSiteID(t, store), endpoint.DisplayName, node.ID, node.ID, meridianruntime.ApplyKind, []byte(`{"endpointId":"`+endpoint.ID+`"}`), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	const executionID = "recover-meridian-execution"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at)
		VALUES(?,?,?,?,1,'old-agent-session','digest',X'01','unknown','started','execution interrupted',?,?,?)`, executionID, node.ID, failedCommandID, "application.command", stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMeridianEndpoint(ctx, endpoint.ID, adminID, MeridianEndpointRecoveryInput{ConfirmCenterAuthority: true, ExecutionStopped: true}); err != nil {
		t.Fatal(err)
	}
	var disposition, actor string
	if err := store.db.QueryRowContext(ctx, `SELECT disposition,disposition_actor FROM task_executions WHERE id=?`, executionID).Scan(&disposition, &actor); err != nil {
		t.Fatal(err)
	}
	if disposition != "meridian-center-recovery" || actor != adminID {
		t.Fatalf("recovery disposition=%q actor=%q", disposition, actor)
	}
	var oldFence int
	if err := store.db.QueryRowContext(ctx, `SELECT reconciliation_required FROM application_commands WHERE id=?`, failedCommandID).Scan(&oldFence); err != nil || oldFence != 0 {
		t.Fatalf("old Meridian fence=%d err=%v", oldFence, err)
	}
	var commandJSON []byte
	if err := store.db.QueryRowContext(ctx, `SELECT input_json FROM application_commands WHERE agent_id=? AND kind=? AND state='pending'`, node.ID, meridianruntime.ApplyKind).Scan(&commandJSON); err != nil {
		t.Fatal(err)
	}
	var command meridianruntime.Command
	if err := json.Unmarshal(commandJSON, &command); err != nil || command.EndpointID != endpoint.ID || !command.ReplacePendingState {
		t.Fatalf("queued recovery command=%#v err=%v", command, err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM meridian_endpoints WHERE id=?`, endpoint.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("recovered endpoint status=%q err=%v", status, err)
	}
}

func TestExpiredMeridianRuntimeLeaseProjectsFailedEndpointForRecovery(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	profile := networking.Profile{ServiceAddress: "10.0.0.46", LANAddress: "10.0.0.46", PublicAddress: "203.0.113.46", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
	node := enrollOrchestrationNode(t, store, "expired-meridian-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := clock.Format(time.RFC3339Nano)
	const applicationID = "expired-meridian-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Expired Meridian entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	verified := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
	endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: "entry.example.test", Name: "Expired entry", RegionCode: "US", VerificationID: verified.VerificationID, TargetIP: verified.TargetIP, TargetHost: verified.TargetHost, ServerName: verified.ServerName})
	if err != nil {
		t.Fatal(err)
	}
	// Endpoint creation records desired state; dispatch queues the projection.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	commandID, err := store.queueMeridianRuntime(ctx, tx, endpoint.ID, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.ID != commandID || task.MeridianRuntime == nil {
		t.Fatalf("claimed Meridian task=%#v err=%v", task, err)
	}
	clock = clock.Add(taskLeaseDuration + time.Second)
	if err := store.recoverExpiredTasks(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	var commandState, endpointStatus, deploymentStatus, lastError string
	var reconciliationRequired, runtimeHealthy int
	if err := store.db.QueryRowContext(ctx, `SELECT state,reconciliation_required,error FROM application_commands WHERE id=?`, commandID).Scan(&commandState, &reconciliationRequired, &lastError); err != nil {
		t.Fatal(err)
	}
	if commandState != "failed" || reconciliationRequired != 1 || !strings.Contains(lastError, "manual verification") {
		t.Fatalf("expired command state=%q reconciliation=%d error=%q", commandState, reconciliationRequired, lastError)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status,runtime_healthy,last_error FROM meridian_endpoints WHERE id=?`, endpoint.ID).Scan(&endpointStatus, &runtimeHealthy, &lastError); err != nil {
		t.Fatal(err)
	}
	if endpointStatus != "failed" || runtimeHealthy != 0 || !strings.Contains(lastError, "manual verification") {
		t.Fatalf("expired endpoint status=%q healthy=%d error=%q", endpointStatus, runtimeHealthy, lastError)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM meridian_deployments WHERE command_id=?`, commandID).Scan(&deploymentStatus); err != nil || deploymentStatus != "failed" {
		t.Fatalf("expired deployment status=%q err=%v", deploymentStatus, err)
	}
}

func TestLegacyRetirementTaskNeverRemovesVerifiedEndpointFromSubscriptions(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	profile := networking.Profile{ServiceAddress: "10.0.0.47", LANAddress: "10.0.0.47", PublicAddress: "203.0.113.47", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
	node := enrollOrchestrationNode(t, store, "retiring-meridian-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := clock.Format(time.RFC3339Nano)
	const applicationID = "retiring-meridian-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Retiring Meridian entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	verified := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
	endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: "entry.example.test", Name: "Retiring entry", RegionCode: "US", VerificationID: verified.VerificationID, TargetIP: verified.TargetIP, TargetHost: verified.TargetHost, ServerName: verified.ServerName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM meridian_deployments WHERE endpoint_id=?`, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM application_commands WHERE agent_id=? AND kind=? AND json_extract(input_json,'$.endpointId')=?`, node.ID, meridianruntime.ApplyKind, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,runtime_healthy=1,status='ready',legacy_retired=0,last_error='' WHERE id=?`, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET status='ready',last_error='' WHERE id=?`, endpoint.ServiceID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	importSecretID, err := store.putSecret(ctx, tx, []byte(`{"complete":true}`), meridianCutoverImportContext)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='retire',subscription_authority='meridian',backup_revision=1,import_sha256=?,import_secret_id=?,expected_endpoints=1,updated_at=? WHERE id=1`, strings.Repeat("a", 64), importSecretID, stamp); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,status='pending',runtime_healthy=0 WHERE id=?`, endpoint.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	recovery, err := store.buildMeridianRuntimeTask(ctx, tx, endpoint.ID, "")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if recovery.task.RetireLegacy || !recovery.task.PreserveLegacyAliases {
		_ = tx.Rollback()
		t.Fatalf("retirement-phase recovery task=%#v", recovery.task)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=desired_revision,status='ready',runtime_healthy=1 WHERE id=?`, endpoint.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	commandID, err := store.queueMeridianRuntime(ctx, tx, endpoint.ID, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertReady := func(stage string) {
		t.Helper()
		var status string
		var healthy int
		if err := store.db.QueryRowContext(ctx, `SELECT status,runtime_healthy FROM meridian_endpoints WHERE id=?`, endpoint.ID).Scan(&status, &healthy); err != nil {
			t.Fatal(err)
		}
		if status != "ready" || healthy != 1 {
			t.Fatalf("%s endpoint status=%q healthy=%d", stage, status, healthy)
		}
	}
	assertReady("queued retirement")
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.ID != commandID || task.MeridianRuntime == nil || !task.MeridianRuntime.RetireLegacy {
		t.Fatalf("claimed retirement task=%#v err=%v", task, err)
	}
	assertReady("claimed retirement")
	clock = clock.Add(taskLeaseDuration + time.Second)
	if err := store.recoverExpiredTasks(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	assertReady("expired retirement")
}

func TestCreateMeridianEndpointReusesRetiredIdentityAndUsage(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	profile := networking.Profile{
		ServiceAddress: "10.0.0.43", LANAddress: "10.0.0.43", PublicAddress: "203.0.113.43",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	}
	node := enrollOrchestrationNode(t, store, "reinstalled-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	const applicationID = "reinstalled-meridian-application"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Reinstalled Meridian entry", node.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	create := func(name string) MeridianEndpointView {
		input := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
		endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: "entry.example.test", Name: name, RegionCode: "US", VerificationID: input.VerificationID, TargetIP: input.TargetIP, TargetHost: input.TargetHost, ServerName: input.ServerName})
		if err != nil {
			t.Fatal(err)
		}
		return endpoint
	}
	first := create("First identity")
	account, err := store.CreateMeridianAccount(ctx, MeridianAccountInput{DisplayName: "Preserved account"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET applied_revision=desired_revision WHERE id=?`, account.Account.ID); err != nil {
		t.Fatal(err)
	}
	var credentialID, oldSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT credential.id,endpoint.private_key_secret_id FROM meridian_credentials credential JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id WHERE credential.account_id=? AND endpoint.id=? AND credential.kind='native'`, account.Account.ID, first.ID).Scan(&credentialID, &oldSecretID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET observed_bytes=123,raw_up_bytes=123 WHERE credential_id=?`, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_credentials SET enabled=0 WHERE endpoint_id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET status='retired',runtime_healthy=0 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET status='stopped' WHERE id=?`, first.ServiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET status='stopped' WHERE service_id=?`, first.ServiceID); err != nil {
		t.Fatal(err)
	}
	second := create("Second identity")
	if second.ID != first.ID || second.ServiceID != first.ServiceID || second.Status != "pending" {
		t.Fatalf("reactivated endpoint=%#v first=%#v", second, first)
	}
	var currentCredentialID string
	var enabled int
	var observed int64
	if err := store.db.QueryRowContext(ctx, `SELECT credential.id,credential.enabled,watermark.observed_bytes FROM meridian_credentials credential JOIN meridian_usage_watermarks watermark ON watermark.credential_id=credential.id WHERE credential.account_id=? AND credential.endpoint_id=? AND credential.kind='native'`, account.Account.ID, second.ID).Scan(&currentCredentialID, &enabled, &observed); err != nil {
		t.Fatal(err)
	}
	if currentCredentialID != credentialID || enabled != 1 || observed != 123 {
		t.Fatalf("credential=%q enabled=%d observed=%d", currentCredentialID, enabled, observed)
	}
	var oldSecretCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id=?`, oldSecretID).Scan(&oldSecretCount); err != nil {
		t.Fatal(err)
	}
	if oldSecretCount != 0 {
		t.Fatalf("old endpoint secret count=%d", oldSecretCount)
	}
	var accountDesired, accountApplied int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision FROM meridian_accounts WHERE id=?`, account.Account.ID).Scan(&accountDesired, &accountApplied); err != nil {
		t.Fatal(err)
	}
	if accountDesired != accountApplied {
		t.Fatalf("reactivating one entry blocked the whole account subscription: desired=%d applied=%d", accountDesired, accountApplied)
	}
}

func TestMeridianRouteLifecycleDoesNotBlockUnrelatedSubscriptionEntries(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	entryProfile := networking.Profile{
		ServiceAddress: "10.0.0.44", LANAddress: "10.0.0.44", PublicAddress: "203.0.113.44",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	}
	entry := enrollOrchestrationNode(t, store, "route-entry", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: entryProfile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: entryProfile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, entryProfile)
	egressProfile := networking.Profile{ServiceAddress: "100.64.0.45", HeadscaleAddress: "100.64.0.45", EnabledKinds: []string{networking.KindHeadscale}}
	egress := enrollOrchestrationNode(t, store, "route-egress", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: egressProfile.ServiceAddress, Interface: "tailscale0", Kind: networking.KindHeadscale}}, egressProfile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	// A private address alone does not make this a managed landing runtime.
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, egress.ID); err != nil {
		t.Fatal(err)
	}
	serverJSON, err := json.Marshal(landing.ServerState{NodeID: egress.ID, Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.45", Sources: []landing.AuthorizedNode{{Address: "100.64.0.44", TCPOnly: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,status,updated_at) VALUES(?,1,1,?,?,'ready',?)`, egress.ID, serverJSON, serverJSON, stamp); err != nil {
		t.Fatal(err)
	}
	const applicationID = "route-lifecycle-meridian-application"
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at)
		VALUES(?,?,?,?)`, entry.ID, landing.ClientRuntimeGeneration, []byte(`{"id":"tailnet-lifecycle-entry","publicKey":"nodekey:test-lifecycle","address":"100.64.0.44"}`), stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'ghcr.io/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558','running','docker','',?,?)`, applicationID, "Route lifecycle Meridian entry", entry.ID, testSiteID(t, store), meridianAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	verification := seedVerifiedRealityInput(t, store, ctx, RealityCommandInput{ApplicationID: applicationID, TargetHost: "www.example.com", ServerName: "www.example.com"})
	endpoint, err := store.CreateMeridianEndpoint(ctx, MeridianEndpointInput{ApplicationID: applicationID, AdvertiseHost: "entry.example.test", Name: "Route lifecycle", RegionCode: "US", VerificationID: verification.VerificationID, TargetIP: verification.TargetIP, TargetHost: verification.TargetHost, ServerName: verification.ServerName})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateMeridianAccount(ctx, MeridianAccountInput{DisplayName: "Stable subscription"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET applied_revision=desired_revision WHERE id=?`, account.Account.ID); err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: account.Account.ID, EndpointID: endpoint.ID, EgressNodeID: egress.ID})
	if err != nil {
		t.Fatal(err)
	}
	assertAccountReady := func(stage string) {
		t.Helper()
		var desired, applied int64
		if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision FROM meridian_accounts WHERE id=?`, account.Account.ID).Scan(&desired, &applied); err != nil {
			t.Fatal(err)
		}
		if desired != applied {
			t.Fatalf("%s blocked the whole account subscription: desired=%d applied=%d", stage, desired, applied)
		}
	}
	assertAccountReady("route creation")
	if err := store.RevokeMeridianRouteGrant(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	assertAccountReady("route revocation")
}
