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
)

type AgentReinstallMonitorRotation struct {
	CommandID string `json:"commandId"`
	State     string `json:"state"`
	NodeID    string `json:"nodeId"`
	RotatedAt string `json:"rotatedAt,omitempty"`
}

type reinstallMonitorRotationCommand struct {
	OperationID    string             `json:"operationId"`
	ApplicationID  string             `json:"applicationId"`
	InspectionID   string             `json:"inspectionId"`
	SourceRevision string             `json:"sourceRevision"`
	Task           pulse.RotationTask `json:"task"`
}

const reinstallRotationAuthoritySQL = `EXISTS(SELECT 1 FROM agent_reinstall_monitor_rotations m
 JOIN agent_reinstall_monitor_inspections i ON i.command_id=m.inspection_id
 JOIN agent_reinstall_operations op ON op.id=m.operation_id JOIN agents target ON target.id=op.agent_id
 WHERE m.command_id=c.id AND c.kind='pulse.node.rotate' AND c.input_json=m.input_json
 AND c.application_id=json_extract(m.input_json,'$.task.inspection.applicationId')
 AND c.gateway_node_id=op.agent_id AND i.operation_id=op.id AND i.application_id=m.application_id
 AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required')
 AND target.status='active' AND target.credential_revoked_at='' AND target.x25519_public_key=i.replacement_key
 AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by))`

func (s *Store) readReinstallMonitorRotation(ctx context.Context, tx *sql.Tx, operationID, applicationID string) (*AgentReinstallMonitorRotation, error) {
	var value AgentReinstallMonitorRotation
	var serviceID, resultJSON string
	err := tx.QueryRowContext(ctx, `SELECT c.id,c.agent_id,CASE WHEN c.state='running' AND (c.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=c.id AND (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' ELSE c.state END,m.result_json,
 json_extract(m.input_json,'$.task.nodeId') FROM agent_reinstall_monitor_rotations m JOIN application_commands c ON c.id=m.command_id WHERE m.operation_id=? AND m.application_id=?`, s.now().UTC().Format(time.RFC3339Nano), operationID, applicationID).Scan(&value.CommandID, &serviceID, &value.State, &resultJSON, &value.NodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if resultJSON != "{}" {
		if err = json.Unmarshal([]byte(resultJSON), &value); err != nil {
			return nil, err
		}
	}
	if _, err = s.validateReinstallMonitorRotation(ctx, tx, serviceID, value.CommandID); err != nil {
		value.State = "needs_review"
	}
	return &value, nil
}

func (s *Store) QueueAgentReinstallMonitorRotation(ctx context.Context, agentID, adminID string, input AgentReinstallMonitorInput) (AgentReinstallMonitorRotation, error) {
	var result AgentReinstallMonitorRotation
	if input.OperationID == "" || input.ApplicationID == "" || len(input.PlanRevision) != 64 {
		return result, errExecutionAuthorization
	}
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	op, err := readAgentReinstallOperation(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if op == nil || op.ID != input.OperationID || op.AuthorizedBy != adminID || op.State != "review_required" || op.PrivateIsolation == "pending" || op.ReplacementFingerprint == "" {
		return result, errExecutionAuthorization
	}
	var key []byte
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT x25519_public_key,status='active' AND credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=?) FROM agents WHERE id=?`, adminID, agentID).Scan(&key, &active); err != nil {
		return result, err
	}
	sum := sha256.Sum256(key)
	if !active || hex.EncodeToString(sum[:]) != op.ReplacementFingerprint {
		return result, errExecutionAuthorization
	}
	// One credential mutation per approved operation/application, including failed
	// or lost results. Refreshes and new plan revisions cannot silently rotate again.
	previous, err := s.readReinstallMonitorRotation(ctx, tx, op.ID, input.ApplicationID)
	if err != nil {
		return result, err
	}
	if previous != nil {
		return *previous, nil
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if input.PlanRevision != plan.Revision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return result, errors.New("center: settle previous machine work before rotating monitoring credentials")
	}
	for _, work := range plan.PendingWork {
		if work.AgentID != agentID {
			return result, errors.New("center: settle related remote work before rotating monitoring credentials")
		}
	}
	index := slices.IndexFunc(plan.Monitoring, func(m AgentReinstallMonitoring) bool { return m.ApplicationID == input.ApplicationID })
	if index < 0 || plan.Monitoring[index].Inspection == nil || plan.Monitoring[index].Inspection.State != "verified" {
		return result, errors.New("center: verify the original monitoring identity before rotating its credential")
	}
	review := plan.Monitoring[index]
	checked, err := time.Parse(time.RFC3339Nano, review.Inspection.InspectedAt)
	if err != nil || checked.After(s.now()) || s.now().Sub(checked) > 30*time.Minute {
		return result, errors.New("center: original monitoring inspection expired; inspect it again")
	}
	inspected, err := s.validateReinstallInspection(ctx, tx, review.ServiceAgentID, review.Inspection.CommandID)
	if err != nil {
		return result, err
	}
	sourcePlan := AgentReinstallPlan{AgentID: agentID}
	sourceRevision, err := readReinstallApplications(ctx, tx, &sourcePlan)
	if err != nil {
		return result, err
	}
	command := reinstallMonitorRotationCommand{OperationID: op.ID, ApplicationID: input.ApplicationID, InspectionID: review.Inspection.CommandID, SourceRevision: sourceRevision,
		Task: pulse.RotationTask{Inspection: inspected.Task, NodeID: review.Inspection.NodeID}}
	if err = command.Task.Validate(); err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id = "reinstall-monitor-rotation-" + id
	raw, _ := json.Marshal(command)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, review.ServiceApplicationID, review.ServiceAgentID, agentID, pulse.RotationKind, raw, now, now); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_monitor_rotations(command_id,operation_id,application_id,inspection_id,plan_revision,input_json) VALUES(?,?,?,?,?,?)`, id, op.ID, input.ApplicationID, command.InspectionID, input.PlanRevision, raw); err != nil {
		return result, err
	}
	if _, err = s.validateReinstallMonitorRotation(ctx, tx, review.ServiceAgentID, id); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, review.ServiceAgentID, "application.command", 1, "queued", "Rotate the verified original monitoring credential; keep the monitoring node and history"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + review.ServiceAgentID)
	return AgentReinstallMonitorRotation{CommandID: id, State: "pending", NodeID: command.Task.NodeID}, nil
}

func (s *Store) validateReinstallMonitorRotation(ctx context.Context, tx *sql.Tx, serviceAgentID, commandID string) (reinstallMonitorRotationCommand, error) {
	var command reinstallMonitorRotationCommand
	var raw []byte
	var target, operationID, applicationID, inspectionID string
	err := tx.QueryRowContext(ctx, `SELECT c.input_json,c.gateway_node_id,m.operation_id,m.application_id,m.inspection_id FROM application_commands c
 JOIN agent_reinstall_monitor_rotations m ON m.command_id=c.id JOIN agents service ON service.id=c.agent_id
 WHERE c.id=? AND c.agent_id=? AND c.attempt<=1 AND (c.state<>'pending' OR c.attempt=0) AND c.reconciliation_requested=0 AND c.reconciliation_required=0
 AND json_extract(service.capabilities_json,'$.pulseRotation')=1 AND `+reinstallRotationAuthoritySQL, commandID, serviceAgentID).Scan(&raw, &target, &operationID, &applicationID, &inspectionID)
	if err != nil {
		return command, errExecutionAuthorization
	}
	if json.Unmarshal(raw, &command) != nil || command.OperationID != operationID || command.ApplicationID != applicationID || command.InspectionID != inspectionID || command.Task.Validate() != nil {
		return command, errExecutionAuthorization
	}
	inspected, err := s.validateReinstallInspection(ctx, tx, serviceAgentID, inspectionID)
	if err != nil {
		return command, err
	}
	expected, _ := json.Marshal(inspected.Task)
	actual, _ := json.Marshal(command.Task.Inspection)
	if string(expected) != string(actual) {
		return command, errExecutionAuthorization
	}
	var receipt AgentReinstallMonitorInspection
	var receiptRaw []byte
	if err = tx.QueryRowContext(ctx, `SELECT i.result_json FROM agent_reinstall_monitor_inspections i JOIN application_commands c ON c.id=i.command_id WHERE i.command_id=? AND c.state='succeeded'`, inspectionID).Scan(&receiptRaw); err != nil {
		return command, errExecutionAuthorization
	}
	if json.Unmarshal(receiptRaw, &receipt) != nil || receipt.State != "verified" || receipt.NodeID != command.Task.NodeID || receipt.CommandID != inspectionID {
		return command, errExecutionAuthorization
	}
	plan := AgentReinstallPlan{AgentID: target}
	revision, err := readReinstallApplications(ctx, tx, &plan)
	if err != nil {
		return command, err
	}
	if revision != command.SourceRevision {
		return command, errExecutionAuthorization
	}
	return command, nil
}

func (s *Store) completeReinstallMonitorRotation(ctx context.Context, commit projectionCommit, tx *sql.Tx, commandID, agentID string, succeeded bool, raw json.RawMessage) error {
	command, err := s.validateReinstallMonitorRotation(ctx, tx, agentID, commandID)
	if err != nil {
		return err
	}
	receipt := AgentReinstallMonitorRotation{CommandID: commandID, State: "needs_review", NodeID: command.Task.NodeID}
	state := "failed"
	if succeeded {
		var result struct {
			Rotation *pulse.RotationResult `json:"pulseRotation"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Rotation == nil || result.Rotation.Validate(command.Task) != nil {
			return errors.New("center: invalid original monitoring credential rotation result")
		}
		// The token remains solely in task_executions.sealed_result. No plaintext
		// credential is copied to command receipts, events, plans or HTTP responses.
		state = "succeeded"
		receipt.State = "rotated"
		receipt.RotatedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	encoded, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_monitor_rotations SET result_json=? WHERE command_id=?`, encoded, commandID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json='{}',error='',lease_expires_at='',updated_at=? WHERE id=?`, state, s.now().UTC().Format(time.RFC3339Nano), commandID); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", 1, state, "Original monitoring credential operation finished; collector restoration and reporting remain pending"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), command.OperationID); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleAgentReinstallMonitorRotation(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallMonitorInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallMonitorRotation(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
