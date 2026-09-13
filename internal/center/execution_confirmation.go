package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
)

// ConfirmExecution applies retained result evidence, never an Agent
// command. The original failed/unknown execution and encrypted evidence remain
// intact; the administrator's disposition is recorded in the same transaction
// as the normal application result validation and business projection.
func (s *Store) ConfirmExecution(ctx context.Context, id, adminID string, input controlplane.ExecutionDisposition) error {
	if input.Action != "confirm-completed" || !input.ExecutionStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		return errors.New("center: confirm stopped execution and record verification")
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
	var agentID, taskID, kind string
	var attempt int64
	var sealedTask, sealedResult []byte
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,task_id,kind,attempt,sealed_task,sealed_result FROM task_executions WHERE id=? AND state IN ('failed','unknown') AND disposition=''`, id).Scan(&agentID, &taskID, &kind, &attempt, &sealedTask, &sealedResult); err != nil {
		return errExecutionAuthorization
	}
	taskRaw, err := secret.Open(s.key, sealedTask, []byte("execution-task:"+id))
	if err != nil {
		return errors.New("center: execution evidence cannot be verified")
	}
	var task AgentTask
	if json.Unmarshal(taskRaw, &task) != nil || task.ID != taskID || task.Kind != kind || task.Attempt != attempt {
		return errExecutionAuthorization
	}
	resultRaw, err := secret.Open(s.key, sealedResult, []byte("execution-result:"+id))
	if err != nil {
		return errors.New("center: retained completion evidence is required")
	}
	var evidence executionResultEvidence
	if json.Unmarshal(resultRaw, &evidence) != nil || !retainedResultSupportsConfirmation(kind, evidence) {
		return errors.New("center: retained result does not prove completion; verify or replace the missing evidence before confirming")
	}
	// Do not overwrite a newer deployment, even if the old attempt still exists.
	// Reopening below is transaction-local and grants no execution authority.
	query, args, err := executionConfirmationStatement(task, agentID)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errors.New("center: application task changed; verify its current state")
	}
	committed := false
	commit := func(tx *sql.Tx) error {
		if err := validateExecutionProjection(ctx, tx, id, true); err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		updated, err := tx.ExecContext(ctx, `UPDATE task_executions SET disposition='confirm-completed',disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND disposition='' AND state IN ('failed','unknown')`, controlplane.SafeError(input.Note), adminID, now, now, id)
		if err != nil {
			return err
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			return errExecutionAuthorization
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	switch kind {
	case "application.command":
		err = s.projectApplicationCommand(ctx, tx, commit, agentID, taskID, attempt, true, "", evidence.Result, false)
	case "application.apply":
		_, _, err = s.projectApplicationDeployment(ctx, tx, agentID, taskID, attempt, true, "", evidence.Result, false, *evidence.ApplicationRuntimeGeneration)
		if err == nil {
			err = commit(tx)
		}
	case "landing.server.apply":
		var result struct {
			LandingPeer *landing.PeerIdentity `json:"landingPeer"`
		}
		if json.Unmarshal(evidence.Result, &result) != nil {
			return errors.New("center: invalid landing service result")
		}
		err = s.projectLandingServer(ctx, tx, commit, agentID, task.Revision, attempt, true, result.LandingPeer)
	case "landing.proxy.apply":
		err = s.projectLandingProxy(ctx, tx, commit, agentID, task.Revision, attempt, true)
	case "gateway.routes.apply":
		err = s.projectGatewayState(ctx, tx, commit, agentID, task.Revision, attempt, true, "")
	case "gateway.component.apply":
		err = s.projectGatewayComponent(ctx, tx, commit, agentID, task.Revision, attempt, true, "")
	case "node.listener.apply":
		err = s.projectNodeListenerState(ctx, tx, commit, agentID, task.Revision, attempt, true, "")
	case "tunnel.state.apply":
		err = s.projectTunnelState(ctx, tx, commit, agentID, task.Revision, attempt, true, "")
	}
	if err != nil {
		return err
	}
	if !committed {
		return errExecutionAuthorization
	}
	return nil
}

func retainedResultSupportsConfirmation(kind string, evidence executionResultEvidence) bool {
	if !evidence.Succeeded || evidence.Unknown || !json.Valid(evidence.Result) {
		return false
	}
	switch kind {
	case "application.apply":
		return evidence.ApplicationRuntimeGeneration != nil
	case "application.command", "landing.server.apply", "landing.proxy.apply", "gateway.routes.apply", "gateway.component.apply", "node.listener.apply", "tunnel.state.apply":
		return true
	default:
		return false
	}
}

func executionConfirmationStatement(task AgentTask, agentID string) (string, []any, error) {
	if task.Kind == "application.command" {
		return `UPDATE application_commands SET state='running' WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed') AND NOT EXISTS(SELECT 1 FROM application_commands newer WHERE newer.application_id=application_commands.application_id AND newer.rowid>application_commands.rowid)`, []any{task.ID, agentID, task.Attempt}, nil
	}
	if task.Kind == "application.apply" {
		return `UPDATE deployments SET state='running' WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','failed') AND NOT EXISTS(SELECT 1 FROM deployments newer WHERE newer.application_id=deployments.application_id AND newer.rowid>deployments.rowid)`, []any{task.ID, agentID, task.Attempt}, nil
	}
	args := []any{agentID, task.Revision, task.Attempt}
	var expectedID, query string
	switch task.Kind {
	case "landing.server.apply":
		expectedID = landingServerTaskID(agentID, task.Revision)
		query = `UPDATE landing_server_states SET status='applying' WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed')`
	case "landing.proxy.apply":
		expectedID = landingProxyTaskID(agentID, task.Revision)
		query = `UPDATE landing_proxy_states SET status='applying' WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed')`
	case "gateway.routes.apply":
		expectedID = gatewayRouteTaskID(agentID, task.Revision)
		query = `UPDATE gateway_states SET status='applying' WHERE gateway_node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed')`
	case "gateway.component.apply":
		expectedID = gatewayComponentTaskID(agentID, task.Revision)
		query = `UPDATE gateway_components SET status='applying' WHERE gateway_node_id=? AND generation=? AND attempt=? AND status IN ('applying','failed')`
	case "node.listener.apply":
		expectedID = nodeListenerTaskID(agentID, task.Revision)
		query = `UPDATE node_listener_states SET status='applying' WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed')`
	case "tunnel.state.apply":
		expectedID = tunnelTaskID(agentID, task.Revision)
		query = `UPDATE cloudflare_tunnels SET status='applying' WHERE agent_id=? AND desired_revision=? AND attempt=? AND status IN ('applying','failed')`
	default:
		return "", nil, errors.New("center: this execution requires its dedicated resolution workflow")
	}
	if task.Revision <= 0 || task.ID != expectedID {
		return "", nil, errExecutionAuthorization
	}
	return query, args, nil
}
