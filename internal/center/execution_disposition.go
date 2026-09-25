package center

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// ReexecuteExecution is an explicit operator decision, not lease recovery.
// Keep the old execution and its result intact; only a subsequent claim may
// create a new attempt and a new one-use authorization.
func (s *Store) ReexecuteExecution(ctx context.Context, executionID, adminID string, input controlplane.ExecutionDisposition) error {
	if input.Action != "reexecute" {
		return errExecutionAuthorization
	}
	return s.disposeTaskExecution(ctx, executionID, adminID, input)
}

// AbandonExecution retains all evidence and credentials, but makes the exact
// business attempt terminal. It never queues another command or undoes effects.
func (s *Store) AbandonExecution(ctx context.Context, executionID, adminID string, input controlplane.ExecutionDisposition) error {
	if input.Action != "abandon" {
		return errExecutionAuthorization
	}
	return s.disposeTaskExecution(ctx, executionID, adminID, input)
}

func (s *Store) disposeTaskExecution(ctx context.Context, executionID, adminID string, input controlplane.ExecutionDisposition) error {
	if !input.ExecutionStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		return errors.New("center: confirm the previous execution stopped and record the verification before reexecution")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return errors.New("center: administrator authorization required")
	}
	var taskID, agentID, kind, state, disposition string
	var attempt int64
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT task_id,agent_id,kind,state,attempt,disposition,sealed_task FROM task_executions WHERE id=?`, executionID).Scan(&taskID, &agentID, &kind, &state, &attempt, &disposition, &sealed); err != nil {
		return err
	}
	if state != "failed" && state != "unknown" || disposition != "" {
		return errors.New("center: execution is not unresolved")
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+executionID))
	if err != nil {
		return errors.New("center: execution evidence cannot be verified")
	}
	var task AgentTask
	if json.Unmarshal(raw, &task) != nil || task.ID != taskID || task.Kind != kind || task.Attempt != attempt {
		return errors.New("center: execution evidence does not match its identity")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	query, args, err := executionRequeueStatement(task, agentID, now)
	if input.Action == "abandon" {
		query, args, err = executionAbandonStatement(task, agentID, now)
	}
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errors.New("center: task changed; verify its current state")
	}
	if kind == "application.apply" {
		applicationStatus := "pending"
		if input.Action == "abandon" {
			applicationStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status=?,updated_at=? WHERE id=(SELECT application_id FROM deployments WHERE id=?)`, applicationStatus, now, taskID); err != nil {
			return err
		}
	}
	updated, err = tx.ExecContext(ctx, `UPDATE task_executions SET disposition=?,disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND disposition='' AND state IN ('failed','unknown')`, input.Action, controlplane.SafeError(input.Note), adminID, now, now, executionID)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return tx.Commit()
}

func executionAbandonStatement(task AgentTask, agentID, now string) (string, []any, error) {
	identity := []any{now, task.ID, agentID, task.Attempt}
	revision := []any{now, agentID, task.Revision, task.Attempt}
	switch task.Kind {
	case "node.ip-quality":
		return `UPDATE ip_quality_checks SET state='failed',lease_expires_at='',error='abandoned',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "node.network-quality", "node.return-route", "node.international-bandwidth", "node.host-profile", "meridian.link-bandwidth", "meridian.link-bandwidth-server":
		return `UPDATE node_diagnostic_checks SET state='failed',lease_expires_at='',error='abandoned',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "xray.configuration.inspect", "xray.configuration.apply":
		return `UPDATE xray_configuration_recoveries SET state='failed',lease_expires_at='',error='abandoned',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "application.apply":
		return `UPDATE deployments SET state='failed',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='Abandoned by operator',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "application.command":
		return `UPDATE application_commands SET state='failed',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='Abandoned by operator',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "landing.proxy.apply":
		return `UPDATE landing_proxy_states SET status='failed',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "landing.server.apply":
		return `UPDATE landing_server_states SET status='failed',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "gateway.routes.apply":
		return `UPDATE gateway_states SET status='failed',lease_expires_at='',updated_at=? WHERE gateway_node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "gateway.component.apply":
		return `UPDATE gateway_components SET status='failed',lease_expires_at='',updated_at=? WHERE gateway_node_id=? AND generation=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "node.listener.apply":
		return `UPDATE node_listener_states SET status='failed',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "tunnel.state.apply":
		return `UPDATE cloudflare_tunnels SET status='failed',lease_expires_at='',updated_at=? WHERE agent_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	default:
		return "", nil, errors.New("center: this execution requires its dedicated resolution workflow")
	}
}

// These statements only reset the exact task revision observed by the operator.
// They never increment attempt; claiming after the decision does that once.
func executionRequeueStatement(task AgentTask, agentID, now string) (string, []any, error) {
	identity := []any{now, task.ID, agentID, task.Attempt}
	revision := []any{now, agentID, task.Revision, task.Attempt}
	switch task.Kind {
	case "node.ip-quality":
		return `UPDATE ip_quality_checks SET state='pending',lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "node.network-quality", "node.return-route", "node.international-bandwidth", "node.host-profile", "meridian.link-bandwidth", "meridian.link-bandwidth-server":
		return `UPDATE node_diagnostic_checks SET state='pending',lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "xray.configuration.inspect", "xray.configuration.apply":
		return `UPDATE xray_configuration_recoveries SET state='pending',lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "application.apply":
		return `UPDATE deployments SET state='pending',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "application.command":
		return `UPDATE application_commands SET state='pending',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending')`, identity, nil
	case "agent.update":
		return `UPDATE agent_updates SET state='pending',lease_expires_at='',last_error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed','pending','installing')`, identity, nil
	case "agent.decommission":
		return `UPDATE agent_decommissions SET state='pending',callback_token_hash=X'',lease_expires_at='',last_error='',updated_at=? WHERE agent_id=? AND attempt=? AND state IN ('running','failed','pending','cleaning')`, []any{now, agentID, task.Attempt}, nil
	case "landing.proxy.apply":
		return `UPDATE landing_proxy_states SET status='pending',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "landing.server.apply":
		return `UPDATE landing_server_states SET status='pending',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "gateway.routes.apply":
		return `UPDATE gateway_states SET status='pending',lease_expires_at='',updated_at=? WHERE gateway_node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "gateway.component.apply":
		return `UPDATE gateway_components SET status='pending',lease_expires_at='',updated_at=? WHERE gateway_node_id=? AND generation=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "node.listener.apply":
		return `UPDATE node_listener_states SET status='pending',lease_expires_at='',updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	case "tunnel.state.apply":
		return `UPDATE cloudflare_tunnels SET status='pending',lease_expires_at='',updated_at=? WHERE agent_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed','pending')`, revision, nil
	default:
		return "", nil, errors.New("center: unsupported execution kind")
	}
}
