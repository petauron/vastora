package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestThreeXUIResetBoundaryUsesSiteLocalMidnight(t *testing.T) {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 18, 30, 0, 0, time.UTC)
	if got := threeXUIResetBoundary(now, 1, location); got != "2026-08-24T16:00:00Z" {
		t.Fatalf("next local midnight boundary = %q", got)
	}
	advanced, err := advanceThreeXUIResetBoundary("2026-08-23T16:00:00Z", 1, now, location)
	if err != nil || advanced != "2026-08-24T16:00:00Z" {
		t.Fatalf("advanced boundary = %q, err=%v", advanced, err)
	}
}

func TestDueThreeXUIInboundPlanResetAdvancesWithRevisionCAS(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := time.Date(2026, 8, 23, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, node)
	completeThreeXUIDeployment(t, store, node, installTask, "10.0.0.80", "controller-token")
	now := clock.Format(time.RFC3339Nano)
	serviceID := "reality-plan-service"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
		VALUES(?, ?, ?, 'inbound-9', 'tcp', 30009, 30009, '10.0.0.80:30009', 'observed', 'vless/tcp/reality', 0, '10.0.0.80', 'ready', ?, ?)`, serviceID, deployment.ApplicationID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	boundary := clock.Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id, inbound_tag, total_bytes, reset_days, next_reset_at, revision, status, updated_at)
		VALUES(?, 'vastora-node-9', 10737418240, 30, ?, 4, 'active', ?)`, serviceID, boundary, now); err != nil {
		t.Fatal(err)
	}
	if err := store.queueDueThreeXUIInboundPlanResets(ctx); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.ClientCommand == nil || task.ClientCommand.Action != "reset_inbound_plan" || task.ClientCommand.ServiceID != serviceID || task.ClientCommand.PlanRevision != 4 || task.ClientCommand.OperationKey != threeXUIInboundResetOperationKey(serviceID, boundary) {
		t.Fatalf("scheduled reset task = %#v", task)
	}
	result, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{
		Inbounds: task.ClientCommand.Inbounds,
	}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	planTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := readThreeXUIInboundPlan(ctx, planTx, serviceID)
	_ = planTx.Rollback()
	if err != nil || plan.Status != "active" || plan.Revision != 4 || plan.LastResetAt == "" {
		t.Fatalf("completed plan = %#v, err=%v", plan, err)
	}
	next, err := time.Parse(time.RFC3339Nano, plan.NextResetAt)
	if err != nil || !next.After(clock) {
		t.Fatalf("next reset boundary = %q, err=%v", plan.NextResetAt, err)
	}

	secondBoundary := clock.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET next_reset_at = ?, status = 'active', retry_at = '', last_error = '' WHERE service_id = ?`, secondBoundary, serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM deployments WHERE application_id = ?`, deployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if err := store.queueDueThreeXUIInboundPlanResets(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || claimed != nil {
		t.Fatalf("unsafe reset claim = %#v, err=%v", claimed, err)
	}
	secondCommandID := "application-command-" + threeXUIInboundResetOperationKey(serviceID, secondBoundary)
	var commandState, commandError string
	if err := store.db.QueryRowContext(ctx, `SELECT state, error FROM application_commands WHERE id = ?`, secondCommandID).Scan(&commandState, &commandError); err != nil {
		t.Fatal(err)
	}
	planTx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = readThreeXUIInboundPlan(ctx, planTx, serviceID)
	_ = planTx.Rollback()
	if err != nil || commandState != "failed" || commandError == "" || plan.Status != "failed" || plan.RetryAt == "" {
		t.Fatalf("failed-closed claim command=%q/%q plan=%#v err=%v", commandState, commandError, plan, err)
	}
}
