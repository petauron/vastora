package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

type AgentReinstallMonitorReporting struct {
	CommandID string `json:"commandId"`
	State     string `json:"state"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

type reinstallMonitorReportingCommand struct {
	OperationID          string `json:"operationId"`
	ApplicationID        string `json:"applicationId"`
	RestorationID        string `json:"restorationId"`
	ServiceApplicationID string `json:"serviceApplicationId"`
	ServiceDeploymentID  string `json:"serviceDeploymentId"`
	TaskDigest           string `json:"taskDigest"`
}

const reinstallReportingAuthoritySQL = `EXISTS(SELECT 1 FROM agent_reinstall_monitor_reports m
 JOIN agent_reinstall_monitor_restorations restore ON restore.deployment_id=m.restoration_id
 JOIN agent_reinstall_monitor_rotations rotation ON rotation.command_id=restore.rotation_command_id
 JOIN agent_reinstall_monitor_inspections inspection ON inspection.command_id=rotation.inspection_id
 JOIN agent_reinstall_operations op ON op.id=rotation.operation_id JOIN agents target ON target.id=op.agent_id
 WHERE m.command_id=c.id AND c.kind='pulse.node.reporting' AND c.input_json=m.input_json
 AND c.gateway_node_id=op.agent_id AND c.application_id=json_extract(m.input_json,'$.serviceApplicationId')
 AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required')
 AND target.status='active' AND target.credential_revoked_at='' AND target.x25519_public_key=inspection.replacement_key
 AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by))`

func (s *Store) buildReinstallReportingTask(ctx context.Context, tx *sql.Tx, target, serviceID string, command reinstallMonitorReportingCommand) (pulse.ReportingTask, error) {
	var result pulse.ReportingTask
	var rotationID string
	err := tx.QueryRowContext(ctx, `SELECT r.command_id FROM agent_reinstall_monitor_restorations m
 JOIN deployments d ON d.id=m.deployment_id JOIN agent_reinstall_monitor_rotations r ON r.command_id=m.rotation_command_id
 JOIN application_commands c ON c.id=r.command_id WHERE d.id=? AND d.agent_id=? AND d.state='succeeded'
 AND r.operation_id=? AND r.application_id=? AND c.agent_id=? AND c.state='succeeded'`, command.RestorationID, target, command.OperationID, command.ApplicationID, serviceID).Scan(&rotationID)
	if err != nil {
		return result, errExecutionAuthorization
	}
	restored, err := s.reinstallMonitorRestoreTask(ctx, tx, target, command.RestorationID, false)
	if err != nil || restored == nil || restored.PulseRestore == nil {
		return result, errExecutionAuthorization
	}
	var executionID string
	var sealed []byte
	if err = tx.QueryRowContext(ctx, `SELECT id,sealed_result FROM task_executions WHERE task_id=? AND agent_id=? AND kind='application.apply' AND attempt=1 AND state='succeeded' AND phase='reported'`, command.RestorationID, target).Scan(&executionID, &sealed); err != nil {
		return result, errExecutionAuthorization
	}
	proof, err := secret.Open(s.key, sealed, []byte("execution-result:"+executionID))
	if err != nil {
		return result, errExecutionAuthorization
	}
	var evidence executionResultEvidence
	var imported struct {
		Restored *pulse.RestoreResult `json:"pulseRestored"`
	}
	if json.Unmarshal(proof, &evidence) != nil || !evidence.Succeeded || evidence.Unknown || json.Unmarshal(evidence.Result, &imported) != nil || imported.Restored == nil || imported.Restored.NodeID != restored.PulseRestore.NodeID {
		return result, errExecutionAuthorization
	}
	rotation, err := s.validateReinstallMonitorRotation(ctx, tx, serviceID, rotationID)
	if err != nil {
		return result, err
	}
	if rotation.Task.Inspection.ApplicationID != command.ServiceApplicationID || rotation.Task.Inspection.DeploymentID != command.ServiceDeploymentID {
		return result, errExecutionAuthorization
	}
	result = pulse.ReportingTask{ApplicationID: command.ServiceApplicationID, DeploymentID: command.ServiceDeploymentID, Credentials: *restored.PulseRestore}
	if result.Validate() != nil {
		return pulse.ReportingTask{}, errExecutionAuthorization
	}
	return result, nil
}

func (s *Store) validateReinstallMonitorReporting(ctx context.Context, tx *sql.Tx, serviceID, commandID string) (reinstallMonitorReportingCommand, pulse.ReportingTask, error) {
	var command reinstallMonitorReportingCommand
	var task pulse.ReportingTask
	var raw []byte
	var target string
	err := tx.QueryRowContext(ctx, `SELECT c.input_json,c.gateway_node_id FROM application_commands c JOIN agents service ON service.id=c.agent_id
 WHERE c.id=? AND c.agent_id=? AND c.attempt<=1 AND (c.state<>'pending' OR c.attempt=0)
 AND c.reconciliation_requested=0 AND c.reconciliation_required=0 AND json_extract(service.capabilities_json,'$.pulseReporting')=1
 AND `+reinstallReportingAuthoritySQL, commandID, serviceID).Scan(&raw, &target)
	if err != nil || json.Unmarshal(raw, &command) != nil {
		return command, task, errExecutionAuthorization
	}
	task, err = s.buildReinstallReportingTask(ctx, tx, target, serviceID, command)
	if err != nil {
		return command, task, err
	}
	encoded, _ := json.Marshal(task)
	sum := sha256.Sum256(encoded)
	if hex.EncodeToString(sum[:]) != command.TaskDigest {
		return command, pulse.ReportingTask{}, errExecutionAuthorization
	}
	return command, task, nil
}

func (s *Store) readReinstallMonitorReporting(ctx context.Context, tx *sql.Tx, restorationID string) (*AgentReinstallMonitorReporting, error) {
	var receipt AgentReinstallMonitorReporting
	var serviceID string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT c.id,c.agent_id,CASE WHEN c.state='running' AND (c.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=c.id AND (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' ELSE c.state END,m.result_json
 FROM agent_reinstall_monitor_reports m JOIN application_commands c ON c.id=m.command_id WHERE m.restoration_id=? ORDER BY m.rowid DESC LIMIT 1`, s.now().UTC().Format(time.RFC3339Nano), restorationID).Scan(&receipt.CommandID, &serviceID, &receipt.State, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if string(raw) != "{}" && json.Unmarshal(raw, &receipt) != nil {
		return nil, errExecutionAuthorization
	}
	_, task, authorityErr := s.validateReinstallMonitorReporting(ctx, tx, serviceID, receipt.CommandID)
	if authorityErr != nil {
		receipt.State = "needs_review"
	}
	if receipt.State == "verified" {
		// CheckedAt is the receipt time, not the last collector sample. Carry
		// forward the sample's age from the authenticated Service observation
		// rather than granting an already-old sample another ten minutes.
		checked, err := time.Parse(time.RFC3339Nano, receipt.CheckedAt)
		now := s.now()
		age, valid := s.reinstallMonitorSampleAge(ctx, tx, serviceID, receipt.CommandID, task, checked)
		if !valid {
			receipt.State = "needs_review"
		} else if err != nil || checked.After(now) || now.Sub(checked) > 10*time.Minute-age {
			receipt.State = "stale"
		}
	}
	return &receipt, nil
}

func (s *Store) reinstallMonitorSampleAge(ctx context.Context, tx *sql.Tx, serviceID, commandID string, task pulse.ReportingTask, checked time.Time) (time.Duration, bool) {
	var executionID, createdAt string
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT id,sealed_result,created_at FROM task_executions
 WHERE task_id=? AND agent_id=? AND kind='application.command' AND attempt=1 AND state='succeeded' AND phase='reported'`, commandID, serviceID).Scan(&executionID, &sealed, &createdAt); err != nil {
		return 0, false
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || created.After(checked) {
		return 0, false
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-result:"+executionID))
	if err != nil {
		return 0, false
	}
	var proof executionResultEvidence
	var result struct {
		Reporting *pulse.ReportingResult `json:"pulseReporting"`
	}
	if json.Unmarshal(raw, &proof) != nil || !proof.Succeeded || proof.Unknown ||
		json.Unmarshal(proof.Result, &result) != nil || result.Reporting == nil || !result.Reporting.FreshAfterRotation(task) {
		return 0, false
	}
	age := time.Duration(result.Reporting.ObservedAt-*result.Reporting.LastSeenAt) * time.Millisecond
	// Service and Center clocks need not agree. Authorization precedes the
	// observation, so this Center-local interval conservatively bounds time
	// spent collecting, delivering and projecting the result. Never reset the
	// freshness window merely because a delayed result was finally received.
	delay := checked.Sub(created)
	if delay > 10*time.Minute-age {
		return 10*time.Minute + time.Nanosecond, true
	}
	return age + delay, true
}

func (s *Store) QueueAgentReinstallMonitorReporting(ctx context.Context, target, adminID string, input AgentReinstallMonitorInput) (AgentReinstallMonitorReporting, error) {
	var receipt AgentReinstallMonitorReporting
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	op, err := readAgentReinstallOperation(ctx, tx, target)
	if err != nil || op == nil || op.ID != input.OperationID || op.AuthorizedBy != adminID || op.State != "review_required" {
		return receipt, errExecutionAuthorization
	}
	plan, err := s.agentReinstallPlan(ctx, tx, target)
	if err != nil {
		return receipt, err
	}
	if plan.Revision != input.PlanRevision {
		return receipt, errors.New("center: recovery plan changed; review it again")
	}
	index := slices.IndexFunc(plan.Monitoring, func(value AgentReinstallMonitoring) bool { return value.ApplicationID == input.ApplicationID })
	if index < 0 || plan.Monitoring[index].Restoration == nil || plan.Monitoring[index].Restoration.State != "succeeded" {
		return receipt, errors.New("center: restore the original collector before verifying its reports")
	}
	review := plan.Monitoring[index]
	previous, err := s.readReinstallMonitorReporting(ctx, tx, review.Restoration.DeploymentID)
	if err != nil {
		return receipt, err
	}
	if previous != nil && (previous.State == "pending" || previous.State == "running" || previous.State == "verified") {
		return *previous, nil
	}
	command := reinstallMonitorReportingCommand{OperationID: op.ID, ApplicationID: input.ApplicationID, RestorationID: review.Restoration.DeploymentID, ServiceApplicationID: review.ServiceApplicationID}
	var serviceTaskRaw []byte
	if err = tx.QueryRowContext(ctx, `SELECT input_json FROM application_commands WHERE id=?`, review.Rotation.CommandID).Scan(&serviceTaskRaw); err != nil {
		return receipt, err
	}
	var rotation reinstallMonitorRotationCommand
	if json.Unmarshal(serviceTaskRaw, &rotation) != nil {
		return receipt, errExecutionAuthorization
	}
	command.ServiceDeploymentID = rotation.Task.Inspection.DeploymentID
	task, err := s.buildReinstallReportingTask(ctx, tx, target, review.ServiceAgentID, command)
	if err != nil {
		return receipt, err
	}
	encoded, _ := json.Marshal(task)
	sum := sha256.Sum256(encoded)
	command.TaskDigest = hex.EncodeToString(sum[:])
	id, err := randomToken(18)
	if err != nil {
		return receipt, err
	}
	id = "reinstall-monitor-report-" + id
	raw, _ := json.Marshal(command)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, review.ServiceApplicationID, review.ServiceAgentID, target, pulse.ReportingKind, raw, now, now); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_monitor_reports(command_id,restoration_id,input_json) VALUES(?,?,?)`, id, command.RestorationID, raw); err != nil {
		return receipt, err
	}
	if _, _, err = s.validateReinstallMonitorReporting(ctx, tx, review.ServiceAgentID, id); err != nil {
		return receipt, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, review.ServiceAgentID, "application.command", 1, "queued", "Verify actual reports from the original monitoring identity"); err != nil {
		return receipt, err
	}
	if err = tx.Commit(); err != nil {
		return receipt, err
	}
	s.taskChanges.notify("agent:" + review.ServiceAgentID)
	return AgentReinstallMonitorReporting{CommandID: id, State: "pending"}, nil
}

func (s *Store) completeReinstallMonitorReporting(ctx context.Context, commit projectionCommit, tx *sql.Tx, commandID, serviceID string, succeeded bool, raw json.RawMessage) error {
	_, task, err := s.validateReinstallMonitorReporting(ctx, tx, serviceID, commandID)
	if err != nil {
		return err
	}
	receipt := AgentReinstallMonitorReporting{CommandID: commandID, State: "needs_review"}
	state := "failed"
	if succeeded {
		var result struct {
			Reporting *pulse.ReportingResult `json:"pulseReporting"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Reporting == nil || result.Reporting.Validate(task) != nil {
			return errors.New("center: invalid original monitoring reporting result")
		}
		state = "succeeded"
		receipt.State = "not_reporting"
		receipt.CheckedAt = s.now().UTC().Format(time.RFC3339Nano)
		if result.Reporting.FreshAfterRotation(task) {
			receipt.State = "verified"
		}
	}
	encoded, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_monitor_reports SET result_json=? WHERE command_id=?`, encoded, commandID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json='{}',error='',lease_expires_at='',updated_at=? WHERE id=?`, state, s.now().UTC().Format(time.RFC3339Nano), commandID); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, commandID, serviceID, "application.command", 1, state, "Original monitoring report inspection finished; business acceptance remains required"); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleAgentReinstallMonitorReporting(w http.ResponseWriter, r *http.Request) {
	var input AgentReinstallMonitorInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallMonitorReporting(r.Context(), r.PathValue("id"), adminID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
