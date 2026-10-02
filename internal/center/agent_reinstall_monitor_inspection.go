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

type AgentReinstallMonitorInput struct {
	OperationID   string `json:"operationId"`
	PlanRevision  string `json:"planRevision"`
	ApplicationID string `json:"applicationId"`
}

type AgentReinstallMonitorInspection struct {
	CommandID   string `json:"commandId"`
	State       string `json:"state"`
	NodeID      string `json:"nodeId,omitempty"`
	Error       string `json:"error,omitempty"`
	InspectedAt string `json:"inspectedAt,omitempty"`
}

type reinstallMonitorCommand struct {
	OperationID      string               `json:"operationId"`
	ApplicationID    string               `json:"applicationId"`
	EvidenceRevision string               `json:"evidenceRevision"`
	Task             pulse.InspectionTask `json:"task"`
}

func monitorEvidenceRevision(review AgentReinstallMonitoring, evidence string) string {
	review.Inspection = nil
	review.Rotation = nil
	raw, _ := json.Marshal(struct {
		Review   AgentReinstallMonitoring
		Evidence string
	}{review, evidence})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func readReinstallInspection(ctx context.Context, tx *sql.Tx, operationID, applicationID string) (*AgentReinstallMonitorInspection, error) {
	var value AgentReinstallMonitorInspection
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT c.id,c.state,i.result_json FROM agent_reinstall_monitor_inspections i JOIN application_commands c ON c.id=i.command_id
	 WHERE i.operation_id=? AND i.application_id=? ORDER BY i.rowid DESC LIMIT 1`, operationID, applicationID).Scan(&value.CommandID, &value.State, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if string(raw) != "{}" {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
	}
	return &value, nil
}

func (s *Store) QueueAgentReinstallMonitorInspection(ctx context.Context, agentID, adminID string, input AgentReinstallMonitorInput) (AgentReinstallMonitorInspection, error) {
	var result AgentReinstallMonitorInspection
	if input.OperationID == "" || len(input.PlanRevision) != 64 || input.ApplicationID == "" {
		return result, errors.New("center: review the original monitoring identity before inspection")
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
		return result, errors.New("center: authorized replacement enrollment and isolation are required before monitoring inspection")
	}
	var key []byte
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT x25519_public_key,status='active' AND credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=?) FROM agents WHERE id=?`, adminID, agentID).Scan(&key, &active); err != nil {
		return result, err
	}
	sum := sha256.Sum256(key)
	if !active || hex.EncodeToString(sum[:]) != op.ReplacementFingerprint {
		return result, errors.New("center: replacement identity changed")
	}
	var savedID string
	err = tx.QueryRowContext(ctx, `SELECT command_id FROM agent_reinstall_monitor_inspections WHERE operation_id=? AND application_id=? AND plan_revision=?`, op.ID, input.ApplicationID, input.PlanRevision).Scan(&savedID)
	if err == nil {
		// Return the exact durable receipt only while its reviewed binding is current.
		var serviceAgentID string
		if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM application_commands WHERE id=?`, savedID).Scan(&serviceAgentID); err != nil {
			return result, err
		}
		if _, err = s.validateReinstallInspection(ctx, tx, serviceAgentID, savedID); err != nil {
			return result, errors.New("center: monitoring review changed; refresh the recovery plan")
		}
		var raw []byte
		if err = tx.QueryRowContext(ctx, `SELECT c.id,c.state,i.result_json FROM agent_reinstall_monitor_inspections i JOIN application_commands c ON c.id=i.command_id WHERE i.command_id=?`, savedID).Scan(&result.CommandID, &result.State, &raw); err != nil {
			return result, err
		}
		if string(raw) != "{}" {
			err = json.Unmarshal(raw, &result)
		}
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if plan.Revision != input.PlanRevision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	index := slices.IndexFunc(plan.Monitoring, func(value AgentReinstallMonitoring) bool { return value.ApplicationID == input.ApplicationID })
	if index < 0 || plan.Monitoring[index].State != "inspection_required" {
		return result, errors.New("center: verified original monitoring registration evidence is required")
	}
	review := plan.Monitoring[index]
	if review.ServiceAgentID == agentID {
		return result, errors.New("center: restore the monitoring service and its data before inspecting its local collector")
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_monitor_inspections i JOIN application_commands c ON c.id=i.command_id WHERE i.operation_id=? AND i.application_id=? AND c.state IN ('pending','running'))`, op.ID, input.ApplicationID).Scan(&pending); err != nil {
		return result, err
	}
	if pending {
		return result, errors.New("center: monitoring inspection is already pending; refresh its saved progress")
	}
	appIndex := slices.IndexFunc(plan.Applications, func(value AgentReinstallApplication) bool { return value.ApplicationID == input.ApplicationID })
	if appIndex < 0 {
		return result, errors.New("center: reviewed monitoring application is missing")
	}
	_, evidence, err := s.reinstallMonitorEvidence(ctx, tx, agentID, plan.Applications[appIndex])
	if err != nil {
		return result, err
	}
	command := reinstallMonitorCommand{OperationID: op.ID, ApplicationID: input.ApplicationID, EvidenceRevision: monitorEvidenceRevision(review, evidence), Task: pulse.InspectionTask{ApplicationID: review.ServiceApplicationID}}
	command.Task.DeploymentID, err = s.reinstallInspectionService(ctx, tx, review.ServiceAgentID, review.ServiceApplicationID)
	if err != nil {
		return result, err
	}
	for _, entry := range review.Enrollments {
		command.Task.EnrollmentIDs = append(command.Task.EnrollmentIDs, entry.EnrollmentID)
	}
	slices.Sort(command.Task.EnrollmentIDs)
	command.Task.EnrollmentIDs = slices.Compact(command.Task.EnrollmentIDs)
	if err = command.Task.Validate(); err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id = "application-command-" + id
	encoded, _ := json.Marshal(command)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, review.ServiceApplicationID, review.ServiceAgentID, agentID, pulse.InspectionKind, encoded, now, now); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_monitor_inspections(command_id,operation_id,application_id,plan_revision,replacement_key,input_json) VALUES(?,?,?,?,?,?)`, id, op.ID, input.ApplicationID, input.PlanRevision, key, encoded); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, review.ServiceAgentID, "application.command", 1, "queued", "Inspect original monitoring identity without changing credentials"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + review.ServiceAgentID)
	return AgentReinstallMonitorInspection{CommandID: id, State: "pending"}, nil
}

func (s *Store) reinstallInspectionService(ctx context.Context, tx *sql.Tx, agentID, applicationID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT d.id FROM applications a JOIN agents n ON n.id=a.node_id
	 JOIN deployments d ON d.rowid=(SELECT latest.rowid FROM deployments latest WHERE latest.application_id=a.id ORDER BY latest.created_at DESC,latest.rowid DESC LIMIT 1)
	 WHERE a.id=? AND a.node_id=? AND a.app_key=? AND a.status='running' AND n.status='active' AND n.credential_revoked_at=''
	 AND json_extract(n.capabilities_json,'$.pulseInspection')=1 AND json_extract(n.capabilities_json,'$.docker')=1
	 AND d.state='succeeded' AND d.operation IN ('install','upgrade','configure') AND d.reconciliation_required=0
	 AND NOT EXISTS(SELECT 1 FROM agent_reinstall_operations r WHERE r.agent_id=n.id AND r.state NOT IN ('superseded','completed'))`, applicationID, agentID, pulseAppKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: a running managed Pulse service with an inspection-capable Agent is required")
	}
	return id, err
}

// Called during selection, final authorization and result projection. An ID in
// the approval table alone cannot authorize changed source material or a task
// for another collector/service/machine generation.
func (s *Store) validateReinstallInspection(ctx context.Context, tx *sql.Tx, serviceAgentID, commandID string) (reinstallMonitorCommand, error) {
	var command reinstallMonitorCommand
	var raw []byte
	var target, operationID, applicationID string
	err := tx.QueryRowContext(ctx, `SELECT c.input_json,c.gateway_node_id,i.operation_id,i.application_id FROM application_commands c
	 JOIN agent_reinstall_monitor_inspections i ON i.command_id=c.id WHERE c.id=? AND c.agent_id=? AND `+reinstallInspectionAuthoritySQL, commandID, serviceAgentID).Scan(&raw, &target, &operationID, &applicationID)
	if err != nil {
		return command, errExecutionAuthorization
	}
	if json.Unmarshal(raw, &command) != nil || command.OperationID != operationID || command.ApplicationID != applicationID || command.Task.Validate() != nil {
		return command, errExecutionAuthorization
	}
	plan := AgentReinstallPlan{AgentID: target}
	if _, err = readReinstallApplications(ctx, tx, &plan); err != nil {
		return command, err
	}
	index := slices.IndexFunc(plan.Applications, func(app AgentReinstallApplication) bool {
		return app.ApplicationID == applicationID && app.Recovery == "reenroll_monitor"
	})
	if index < 0 {
		return command, errExecutionAuthorization
	}
	review, evidence, err := s.reinstallMonitorEvidence(ctx, tx, target, plan.Applications[index])
	if err != nil {
		return command, err
	}
	if review.State != "inspection_required" || review.ServiceAgentID != serviceAgentID || review.ServiceApplicationID != command.Task.ApplicationID || monitorEvidenceRevision(review, evidence) != command.EvidenceRevision {
		return command, errExecutionAuthorization
	}
	expected := []string{}
	for _, entry := range review.Enrollments {
		expected = append(expected, entry.EnrollmentID)
	}
	slices.Sort(expected)
	expected = slices.Compact(expected)
	if !slices.Equal(expected, command.Task.EnrollmentIDs) {
		return command, errExecutionAuthorization
	}
	deploymentID, err := s.reinstallInspectionService(ctx, tx, serviceAgentID, review.ServiceApplicationID)
	if err != nil {
		return command, err
	}
	if deploymentID != command.Task.DeploymentID {
		return command, errExecutionAuthorization
	}
	return command, nil
}

func (s *Store) completeReinstallInspection(ctx context.Context, commit projectionCommit, tx *sql.Tx, commandID, agentID string, succeeded bool, raw json.RawMessage) error {
	command, err := s.validateReinstallInspection(ctx, tx, agentID, commandID)
	if err != nil {
		return err
	}
	receipt := AgentReinstallMonitorInspection{CommandID: commandID, State: "failed", Error: "Monitoring inspection failed; inspect the service and explicitly retry"}
	state := "failed"
	if succeeded {
		var result struct {
			Inspection *pulse.InspectionResult `json:"pulseInspection"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Inspection == nil || result.Inspection.Validate(command.Task) != nil {
			return errors.New("center: invalid original monitoring inspection result")
		}
		state = "succeeded"
		receipt.InspectedAt = s.now().UTC().Format(time.RFC3339Nano)
		receipt.NodeID, err = result.Inspection.OriginalNodeID(command.Task)
		if err != nil {
			receipt.State = "needs_review"
			receipt.Error = "Original monitoring identity is missing, inactive or ambiguous"
		} else {
			receipt.State = "verified"
			receipt.Error = ""
		}
	}
	encoded, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_monitor_inspections SET result_json=? WHERE command_id=?`, encoded, commandID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json='{}',error=?,lease_expires_at='',updated_at=? WHERE id=?`, state, receipt.Error, s.now().UTC().Format(time.RFC3339Nano), commandID); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", 1, state, "Original monitoring inspection finished; credentials and restoration remain unchanged"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), command.OperationID); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleAgentReinstallMonitorInspection(writer http.ResponseWriter, request *http.Request) {
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
	result, err := s.store.QueueAgentReinstallMonitorInspection(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
