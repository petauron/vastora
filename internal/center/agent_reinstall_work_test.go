package center

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func addReinstallRemoteCommand(t *testing.T, store *Store, target AgentCredential) AgentCredential {
	t.Helper()
	ctx := context.Background()
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "Monitoring host", CenterURL: "https://center.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := store.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	addReinstallApplication(t, store, monitor, "monitor", pulseAppKey, "0.1.0-alpha.5", "install", "succeeded")
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
	 VALUES('remote-enrollment','monitor',?,?,'pulse.enrollment.create',?,'pending',?,?)`, monitor.ID, target.ID,
		`{"applicationId":"monitor","deploymentId":"collector-deployment","private":"private-input-never-return"}`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return monitor
}

func TestAgentReinstallPlanIncludesRemoteTargetWorkWithoutRetiringIt(t *testing.T) {
	store, node := prepareReinstallNode(t)
	monitor := addReinstallRemoteCommand(t, store, node)
	ctx := context.Background()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	for _, item := range []struct{ id, agent, task, kind string }{
		{"related", monitor.ID, "remote-enrollment", "application.command"},
		{"wrong-owner", node.ID, "remote-enrollment", "application.command"},
		{"wrong-kind", monitor.ID, "remote-enrollment", "node.ip-quality"},
		{"unrelated", monitor.ID, "another-task", "application.command"},
	} {
		if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at)
		 VALUES(?,?,?,?,?,'session','digest',X'1234',?,'unknown','result_received',?,?,?)`, item.id, item.agent, item.task, item.kind,
			len(item.id), []byte("sealed-result-never-return"), stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// Local pending work on the monitoring host must not leak into the target's
	// review merely because that host also has a related command.
	if _, err := store.db.Exec(`UPDATE deployments SET state='pending' WHERE agent_id=?`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PendingWork) != 1 || plan.PendingWork[0] != (AgentReinstallPendingWork{AgentID: monitor.ID, Kind: "pulse.enrollment.create", Count: 1}) {
		t.Fatalf("wrong target work scope: %+v", plan.PendingWork)
	}
	if len(plan.Executions) != 2 || !slices.ContainsFunc(plan.Executions, func(e AgentReinstallExecution) bool {
		return e.ID == "related" && e.AgentID == monitor.ID && e.TaskID == "remote-enrollment" && e.Attempt == int64(len("related")) && !e.IdentityRetired
	}) || !slices.Contains(plan.Requirements, "inspect_remote_effects_before_restore") {
		t.Fatalf("remote execution was omitted or confused with the replaced identity: %+v", plan.Executions)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-input-never-return", "sealed-result-never-return", "input_json", "sealed_result"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("review leaked %s", forbidden)
		}
	}
	if _, err := createReviewedReconnect(t, store, ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	var retired, state string
	var result []byte
	if err := store.db.QueryRow(`SELECT identity_retired_at,state,sealed_result FROM task_executions WHERE id='related'`).Scan(&retired, &state, &result); err != nil || retired != "" || state != "unknown" || string(result) != "sealed-result-never-return" {
		t.Fatalf("remote evidence/identity changed: retired=%q state=%q err=%v", retired, state, err)
	}
}

func TestAgentReinstallReviewBindsExactPendingWork(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE application_commands SET id='replacement-command' WHERE id='remote-enrollment'`,
		`UPDATE application_commands SET attempt=attempt+1 WHERE id='remote-enrollment'`,
		`UPDATE application_commands SET state='running' WHERE id='remote-enrollment'`,
		`UPDATE application_commands SET input_json=json_set(input_json,'$.deploymentId','another-deployment') WHERE id='remote-enrollment'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			store, node := prepareReinstallNode(t)
			addReinstallRemoteCommand(t, store, node)
			ctx := context.Background()
			input := reviewedReconnectInput(t, store, node.ID)
			before, err := store.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			after, err := store.AgentReinstallPlan(ctx, node.ID)
			if err != nil || before.Revision == after.Revision || !reflect.DeepEqual(before.PendingWork, after.PendingWork) {
				t.Fatalf("same-count intent change did not invalidate review: before=%+v after=%+v err=%v", before.PendingWork, after.PendingWork, err)
			}
			if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil {
				t.Fatal("stale review authorized machine replacement")
			}
			if err := store.authenticateAgent(ctx, node.ID, node.Credential); err != nil {
				t.Fatalf("rejected review revoked credential: %v", err)
			}
		})
	}
}

func TestAgentReinstallWorkRevisionIgnoresUnrelatedWorkAndLeaseRefresh(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	monitor := addReinstallRemoteCommand(t, store, node)
	ctx := context.Background()
	before, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE application_commands SET lease_expires_at='2099-01-01T00:00:00Z',updated_at='2099-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE deployments SET state='pending',attempt=attempt+1 WHERE agent_id=?`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("irrelevant update invalidated review: %v", err)
	}
}

func TestAgentReinstallWorkRevisionBindsConfigurationAndExecutionEvidence(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE deployments SET config_json='{"private":"changed"}'`,
		`UPDATE gateway_states SET desired_revision=desired_revision+1`,
		`UPDATE gateway_states SET desired_json='{"changed":true}'`,
		`UPDATE task_executions SET sealed_result=X'11223344'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			store, node := reinstallPlanFixture(t)
			addReinstallApplication(t, store, node, "saved", meridianAppKey, "0.1.0-alpha.12", "install", "pending")
			if _, err := store.db.Exec(`INSERT INTO gateway_states(gateway_node_id,desired_revision,desired_json,status,updated_at) VALUES(?,1,'{}','pending','')`, node.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at)
			 VALUES('retained',?,'previous-task','application.command',1,'session','digest',X'1234','unknown','result_received','','','')`, node.ID); err != nil {
				t.Fatal(err)
			}
			before, err := store.AgentReinstallPlan(context.Background(), node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			after, err := store.AgentReinstallPlan(context.Background(), node.ID)
			if err != nil || after.Revision == before.Revision || !reflect.DeepEqual(before.PendingWork, after.PendingWork) || !reflect.DeepEqual(before.Executions, after.Executions) {
				t.Fatalf("hidden intent/evidence change did not invalidate review: %v", err)
			}
		})
	}
}
