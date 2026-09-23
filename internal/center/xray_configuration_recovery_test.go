package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/xrayrecovery"
)

func TestXrayConfigurationRecoveryRequiresInspectionBeforeAuthoritySelection(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "xray-recovery", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,runtime,role,status,created_at,updated_at) VALUES('xray-app','Xray',?,(SELECT site_id FROM agents WHERE id=?),'vastora-official/3x-ui','docker','worker','failed',?,?)`, node.ID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.StartXrayConfigurationApply(ctx, node.ID, xrayrecovery.AgentSource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("authority accepted without inspection: %v", err)
	}
	if err := store.StartXrayConfigurationInspection(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	claim := func() *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		task, err := store.claimXrayConfigurationRecovery(ctx, tx, node.ID)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	inspectionTask := claim()
	result := xrayrecovery.Result{Action: "inspect", ApplicationID: "xray-app", RuntimeSHA256: digestFixture('a'), AgentSHA256: digestFixture('b'), AgentRevision: 7, RuntimeImportable: true, RuntimeInbounds: []xrayrecovery.InboundSummary{}, AgentInbounds: []xrayrecovery.InboundSummary{}}
	raw, _ := json.Marshal(map[string]any{"xrayRecovery": result})
	if err := store.completeXrayConfigurationRecovery(ctx, commitProjectionOnlyForTest, node.ID, inspectionTask.ID, inspectionTask.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
	view, err := store.XrayConfigurationRecovery(ctx, node.ID)
	if err != nil || view.State != "awaiting_decision" || view.Result == nil {
		t.Fatalf("inspection view=%#v err=%v", view, err)
	}
	var event string
	if err := store.db.QueryRowContext(ctx, `SELECT event FROM task_events WHERE task_id=? ORDER BY created_at DESC LIMIT 1`, inspectionTask.ID).Scan(&event); err != nil || event != "succeeded" {
		t.Fatalf("inspection completion event=%q err=%v", event, err)
	}
	if err := store.StartXrayConfigurationApply(ctx, node.ID, xrayrecovery.AgentSource); err != nil {
		t.Fatal(err)
	}
	applyTask := claim()
	if applyTask.Kind != xrayrecovery.ApplyKind || applyTask.XrayRecovery.ExpectedAgentRevision != 7 || applyTask.XrayRecovery.ExpectedRuntimeSHA256 != result.RuntimeSHA256 {
		t.Fatalf("apply task=%#v", applyTask)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,reconciliation_required,attempt,error,created_at,updated_at)
		VALUES('xray-drift-command','xray-app',?,?,'3xui.node.reconcile','{}','failed',1,1,'unknown result',?,?)`, node.ID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at)
		VALUES('xray-drift-execution',?,'xray-drift-command','application.command',1,'prior-session','digest',X'01','failed','reported','unknown result',?,?,?)`, node.ID, now, now, now); err != nil {
		t.Fatal(err)
	}
	applied := xrayrecovery.Result{Action: xrayrecovery.AgentSource, AppliedSource: xrayrecovery.AgentSource, ApplicationID: "xray-app", RuntimeSHA256: digestFixture('b'), AgentSHA256: digestFixture('b'), AgentRevision: 8, Matches: true}
	appliedRaw, _ := json.Marshal(map[string]any{"xrayRecovery": applied})
	if err := store.completeXrayConfigurationRecovery(ctx, commitProjectionOnlyForTest, node.ID, applyTask.ID, applyTask.Attempt, true, "", appliedRaw); err != nil {
		t.Fatal(err)
	}
	var disposition string
	var reconciliationRequired int
	if err := store.db.QueryRowContext(ctx, `SELECT disposition FROM task_executions WHERE id='xray-drift-execution'`).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT reconciliation_required FROM application_commands WHERE id='xray-drift-command'`).Scan(&reconciliationRequired); err != nil {
		t.Fatal(err)
	}
	if disposition != "configuration-recovered" || reconciliationRequired != 0 {
		t.Fatalf("recovery left the old execution fenced: disposition=%q required=%d", disposition, reconciliationRequired)
	}
}

func TestXrayConfigurationRecoveryFindsLegacyWorkerDuringMeridianCutover(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='verify',subscription_authority='meridian' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var nodeID string
	if err := store.db.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartXrayConfigurationInspection(ctx, nodeID); err != nil {
		t.Fatalf("legacy worker inspection was hidden by its Meridian application key: %v", err)
	}
	view, err := store.XrayConfigurationRecovery(ctx, nodeID)
	if err != nil || view.ApplicationID != "snapshot-shared-app" || view.State != "pending" {
		t.Fatalf("cutover recovery=%#v err=%v", view, err)
	}
}

func digestFixture(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
