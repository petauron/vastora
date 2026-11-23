package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/secret"
)

func TestPublicAddressRecoveryAllowsArchivingSupersededGatewayExecution(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	capabilities := testRuntimeCapabilities(NodeCapabilities{Docker: true, Gateway: true})
	profile := networking.Profile{
		ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63",
		PublicAddress: "203.0.113.19", PublicBindAddress: "203.0.113.19", PublicMode: networking.PublicModeDirect,
		EnabledKinds: []string{networking.KindHeadscale, networking.KindPublic}, DirectPublic: true,
	}
	candidates := []networking.Candidate{
		{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "192.168.1.70", Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}
	node := enrollOrchestrationNode(t, store, "address-recovery-gateway", capabilities, candidates, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO gateway_components(gateway_node_id,desired_status,generation,applied_generation,status,updated_at)
		VALUES(?,'running',1,1,'ready',?) ON CONFLICT(gateway_node_id) DO UPDATE SET desired_status='running',applied_generation=generation,status='ready'`, node.ID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO gateway_states(gateway_node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at)
		VALUES(?,7,6,?,'applying',3,?) ON CONFLICT(gateway_node_id) DO UPDATE SET desired_revision=7,applied_revision=6,desired_json=excluded.desired_json,status='applying',attempt=3,updated_at=excluded.updated_at`,
		node.ID, []byte(`{"revision":7,"listeners":[],"routes":[]}`), stamp); err != nil {
		t.Fatal(err)
	}
	const oldExecution = "superseded-gateway-execution"
	oldTask := AgentTask{ID: gatewayRouteTaskID(node.ID, 7), Kind: "gateway.routes.apply", Attempt: 3, Revision: 7}
	payload, err := json.Marshal(oldTask)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(store.key, payload, []byte("execution-task:"+oldExecution))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,last_error,expires_at,created_at,updated_at)
		VALUES(?,?,?,?,?,'old-session','digest',?,X'1234','unknown','result_received','expired authorization',?,?,?)`,
		oldExecution, node.ID, oldTask.ID, oldTask.Kind, oldTask.Attempt, sealed, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	candidates[2].Address = "198.51.100.11"
	observation := networking.PublicEgress{Address: candidates[2].Address, BindAddress: candidates[2].Address, Mode: networking.PublicModeDirect, ObservedAt: store.now().UTC()}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: capabilities, GatewayHealthy: true,
		NetworkCandidates: candidates, PublicEgress: &observation, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, Startup: true,
	}); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatalf("list recovered Agent: %v (%d nodes)", err, len(agents))
	}
	retained := agents[0].NetworkRecoveryProfile
	if agents[0].NetworkProfile != nil || retained == nil || retained.PublicAddress != profile.PublicAddress ||
		retained.ServiceAddress != profile.ServiceAddress || retained.LANAddress != "" {
		t.Fatal("retained profile was lost or advertised as active")
	}
	profile.PublicAddress, profile.PublicBindAddress = observation.Address, observation.BindAddress
	if confirmed, err := store.ConfirmNetworkProfile(ctx, node.ID, profile); err != nil || confirmed == nil || confirmed.PublicAddress != observation.Address {
		t.Fatalf("confirm observed public mapping: %#v %v", confirmed, err)
	}

	const session = "replacement-gateway-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	if task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); task != nil || !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("old execution released before review: %#v %v", task, err)
	}
	cookie, _, err := store.CreateFirstAdmin(ctx, "gateway-recovery-admin", "test-only-strong-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.SessionAdminID(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	decision := controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Verified the earlier executor stopped"}
	if err := store.ReexecuteExecution(ctx, oldExecution, admin, decision); err == nil {
		t.Fatal("superseded gateway work was replayed")
	}
	decision.Action = "abandon"
	decision.ExecutionStopped = false
	if err := store.AbandonExecution(ctx, oldExecution, admin, decision); err == nil {
		t.Fatal("superseded work was released without stopped verification")
	}
	decision.ExecutionStopped = true
	if err := store.AbandonExecution(ctx, oldExecution, admin, decision); err != nil {
		t.Fatal(err)
	}
	var state, phase, disposition, actor string
	var retainedTask, retainedResult []byte
	if err := store.db.QueryRow(`SELECT state,phase,disposition,disposition_actor,sealed_task,sealed_result FROM task_executions WHERE id=?`, oldExecution).
		Scan(&state, &phase, &disposition, &actor, &retainedTask, &retainedResult); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || phase != "result_received" || disposition != "abandon" || actor != admin ||
		!bytes.Equal(retainedTask, sealed) || !bytes.Equal(retainedResult, []byte{0x12, 0x34}) {
		t.Fatal("old execution evidence or operator attribution was lost")
	}
	task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || task == nil || task.ID != gatewayRouteTaskID(node.ID, 8) || task.Attempt != 4 {
		t.Fatalf("new gateway revision was not claimed: %#v %v", task, err)
	}
}
