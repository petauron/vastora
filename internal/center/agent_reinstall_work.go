package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// This exception authorizes only the immutable, operation-bound read command.
// Fresh source evidence is checked again by Store before issuance/projection.
const reinstallInspectionAuthoritySQL = `EXISTS(SELECT 1 FROM agent_reinstall_monitor_inspections i
 JOIN agent_reinstall_operations op ON op.id=i.operation_id JOIN agents target ON target.id=op.agent_id
 WHERE i.command_id=c.id AND c.kind='pulse.enrollment.inspect' AND c.input_json=i.input_json
 AND c.application_id=json_extract(i.input_json,'$.task.applicationId')
 AND c.gateway_node_id=op.agent_id AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required')
 AND target.status='active' AND target.credential_revoked_at='' AND target.x25519_public_key=i.replacement_key
 AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by))`

const reinstallRuntimeAuthoritySQL = `EXISTS(SELECT 1 FROM agent_reinstall_app_preparations p
 JOIN agent_reinstall_operations op ON op.id=p.operation_id JOIN agents n ON n.id=op.agent_id
 WHERE p.runtime_command_id=c.id AND c.kind='meridian.runtime.apply' AND c.agent_id=op.agent_id AND c.gateway_node_id=op.agent_id AND c.application_id=p.application_id
 AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required') AND n.status='active' AND n.credential_revoked_at=''
 AND n.x25519_public_key=p.replacement_key AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by))`

func reinstallCommandTargetBlocked(ctx context.Context, q networkQueryer, agentID, taskID string) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_commands c
	 JOIN agent_reinstall_operations r ON r.agent_id=c.gateway_node_id
	 WHERE c.id=? AND c.agent_id=? AND r.state NOT IN ('superseded','completed') AND NOT (`+reinstallInspectionAuthoritySQL+` OR `+reinstallRotationAuthoritySQL+` OR `+reinstallReportingAuthoritySQL+` OR `+reinstallAcceptanceAuthoritySQL+` OR `+reinstallRuntimeAuthoritySQL+`))`, taskID, agentID).Scan(&blocked)
	return blocked, err
}

// Stop outstanding authority over the recovering target in the same transaction
// as local identity retirement. Remote hosts keep their identity and session;
// an unknown outcome is retained for inspection, never interpreted as rollback.
func pauseReinstallRelatedCommands(ctx context.Context, tx *sql.Tx, agentID, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='unknown',expires_at=?,updated_at=?,
	 last_error=CASE WHEN last_error='' THEN 'Command target is being reinstalled; inspect the remote outcome' ELSE last_error END
	 WHERE agent_id<>? AND kind='application.command' AND disposition='' AND state IN ('offered','running','helper_running')
	 AND EXISTS(SELECT 1 FROM application_commands c WHERE c.id=task_executions.task_id
	 AND c.agent_id=task_executions.agent_id AND c.gateway_node_id=?)`, now, now, agentID, agentID)
	return err
}

// Read within the plan's transaction. Counts are presentation only: the review
// revision binds the exact tasks, attempts, intent and retained result evidence.
// Raw configuration and sealed evidence must never leave this function.
func (s *Store) readReinstallWork(ctx context.Context, tx *sql.Tx, plan *AgentReinstallPlan) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	remote := false
	var unclaimed []AgentReinstallUnclaimedWork
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.agent_id,e.task_id,e.attempt,e.kind,e.state,e.phase,e.identity_retired_at<>'',e.digest,e.sealed_result
		FROM task_executions e WHERE NOT EXISTS(SELECT 1 FROM agent_reinstall_client_checks a JOIN agent_reinstall_operations op ON op.id=a.operation_id WHERE a.command_id=e.task_id AND op.agent_id=? AND op.state='review_required') AND e.disposition='' AND e.state<>'succeeded' AND (e.agent_id=? OR
		(e.kind='application.command' AND EXISTS(SELECT 1 FROM application_commands c
		 WHERE c.id=e.task_id AND c.agent_id=e.agent_id AND c.gateway_node_id=?))) ORDER BY e.created_at,e.id`, plan.AgentID, plan.AgentID, plan.AgentID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var execution AgentReinstallExecution
		var taskDigest string
		var result []byte
		if err := rows.Scan(&execution.ID, &execution.AgentID, &execution.TaskID, &execution.Attempt, &execution.Kind, &execution.State, &execution.Phase, &execution.IdentityRetired, &taskDigest, &result); err != nil {
			rows.Close()
			return "", err
		}
		if err := encoder.Encode([]any{execution, taskDigest, result}); err != nil {
			rows.Close()
			return "", err
		}
		remote = remote || execution.AgentID != plan.AgentID
		plan.Executions = append(plan.Executions, execution)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for i := range plan.Executions {
		execution := &plan.Executions[i]
		execution.Resolution = "manual_review"
		if execution.AgentID != plan.AgentID || !execution.IdentityRetired {
			continue
		}
		task, err := s.reinstallLocalExecutionTask(ctx, tx, *execution)
		if err != nil {
			return "", err
		}
		if task != nil {
			execution.Resolution = "local_after_isolation"
		}
	}
	// Include unclaimed intents as well as executing work. In particular, Pulse
	// enrollment runs on the monitoring host but targets the collector being
	// replaced. It is remote work, not an execution identity to retire locally.
	rows, err = tx.QueryContext(ctx, `SELECT kind,agent_id,id,state,attempt,revision,intent FROM (
		SELECT 'application.apply' AS kind,agent_id,id,state,attempt,0 AS revision,
		 json_array(application_id,app_key,app_version,CAST(manifest_json AS TEXT),CAST(config_json AS TEXT),operation,delete_data,
		 service_address,secret_id,registry_credential_id,runtime_generation,reconciliation_required,reconciliation_requested) AS intent
		 FROM deployments WHERE state IN ('pending','running') OR reconciliation_required=1
		UNION ALL SELECT kind,agent_id,id,state,attempt,0,
		 json_array(application_id,gateway_node_id,CAST(input_json AS TEXT),CAST(result_json AS TEXT),result_secret_id,reconciliation_required,reconciliation_requested)
		 FROM application_commands WHERE NOT EXISTS(SELECT 1 FROM agent_reinstall_client_checks a JOIN agent_reinstall_operations op ON op.id=a.operation_id WHERE a.command_id=application_commands.id AND op.agent_id=application_commands.gateway_node_id AND op.state='review_required') AND (state IN ('pending','running') OR reconciliation_required=1) AND (agent_id=? OR gateway_node_id=?)
		UNION ALL SELECT 'gateway.component.apply',gateway_node_id,gateway_node_id,status,attempt,generation,json_array(desired_status,applied_generation)
		 FROM gateway_components WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'gateway.routes.apply',gateway_node_id,gateway_node_id,status,attempt,desired_revision,json_array(CAST(desired_json AS TEXT),applied_revision)
		 FROM gateway_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'node.listener.apply',node_id,node_id,status,attempt,desired_revision,json_array(CAST(desired_json AS TEXT),applied_revision)
		 FROM node_listener_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'landing.server.apply',node_id,node_id,status,attempt,desired_revision,json_array(CAST(desired_json AS TEXT),applied_revision)
		 FROM landing_server_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'landing.proxy.apply',node_id,node_id,status,attempt,desired_revision,json_array(CAST(desired_json AS TEXT),applied_revision)
		 FROM landing_proxy_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'tunnel.state.apply',agent_id,agent_id,status,attempt,desired_revision,json_array(tunnel_id,token_secret_id,CAST(desired_json AS TEXT),applied_revision)
		 FROM cloudflare_tunnels WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'agent.update',agent_id,id,state,attempt,0,json_array(target_version)
		 FROM agent_updates WHERE state IN ('pending','running','installing')
		UNION ALL SELECT 'agent.decommission',agent_id,agent_id,state,attempt,0,json_array(delete_data)
		 FROM agent_decommissions WHERE state IN ('pending','running','cleaning')
		UNION ALL SELECT CASE WHEN action='inspect' THEN 'xray.configuration.inspect' ELSE 'xray.configuration.apply' END,agent_id,id,state,attempt,expected_agent_revision,
		 json_array(application_id,action,expected_runtime_sha256,expected_agent_sha256,result_json) FROM xray_configuration_recoveries WHERE state<>'succeeded'
		UNION ALL SELECT 'node.ip-quality',agent_id,id,state,attempt,0,json_array(address,bind_address) FROM ip_quality_checks WHERE state IN ('pending','running')
		UNION ALL SELECT kind,agent_id,id,state,attempt,target_revision,json_array(bind_address,targets_json,pair_key) FROM node_diagnostic_checks WHERE state IN ('pending','running')
	) work WHERE work.agent_id=? OR EXISTS(SELECT 1 FROM application_commands c WHERE c.id=work.id AND c.kind=work.kind AND c.agent_id=work.agent_id AND c.gateway_node_id=?)
	ORDER BY kind,agent_id,id`, plan.AgentID, plan.AgentID, plan.AgentID, plan.AgentID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, agentID, id, state, intent string
		var attempt, revision int64
		if err := rows.Scan(&kind, &agentID, &id, &state, &attempt, &revision, &intent); err != nil {
			return "", err
		}
		if err := encoder.Encode([]any{kind, agentID, id, state, attempt, revision, intent}); err != nil {
			return "", err
		}
		remote = remote || agentID != plan.AgentID
		if agentID == plan.AgentID && state == "pending" && attempt == 0 {
			if taskID := reinstallUnclaimedTaskID(kind, id, revision); taskID != "" {
				unclaimed = append(unclaimed, AgentReinstallUnclaimedWork{TaskID: taskID, Kind: kind, Revision: revision})
			}
		}
		last := len(plan.PendingWork) - 1
		if last >= 0 && plan.PendingWork[last].Kind == kind && plan.PendingWork[last].AgentID == agentID {
			plan.PendingWork[last].Count++
		} else {
			plan.PendingWork = append(plan.PendingWork, AgentReinstallPendingWork{AgentID: agentID, Kind: kind, Count: 1})
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	for _, work := range unclaimed {
		var authorized bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND task_id=?) OR EXISTS(SELECT 1 FROM agent_reinstall_app_preparations WHERE deployment_id=? OR listener_task_id=?) OR EXISTS(SELECT 1 FROM agent_reinstall_monitor_restorations WHERE deployment_id=?)`, plan.AgentID, work.TaskID, work.TaskID, work.TaskID, work.TaskID).Scan(&authorized); err != nil {
			return "", err
		}
		// Attempt zero alone is insufficient: a previously authorized task may
		// have been reset. Every historical authorization, even disposed, rules
		// out cancellation as unissued work.
		if !authorized {
			plan.UnclaimedLocalWork = append(plan.UnclaimedLocalWork, work)
		}
	}
	if len(plan.PendingWork)+len(plan.Executions) > 0 {
		plan.Requirements = append(plan.Requirements, "inspect_previous_effects_and_generate_fresh_plan")
	}
	if remote {
		plan.Requirements = append(plan.Requirements, "inspect_remote_effects_before_restore")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
