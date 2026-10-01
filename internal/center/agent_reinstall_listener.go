package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"
)

type AgentReinstallListener struct {
	TaskID string `json:"taskId"`
	State  string `json:"state"`
}

func (s *Store) readReinstallListener(ctx context.Context, tx *sql.Tx, preparationID string) (*AgentReinstallListener, error) {
	var result AgentReinstallListener
	err := tx.QueryRowContext(ctx, `SELECT p.listener_task_id,CASE WHEN p.listener_state='running' AND
 (n.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=p.listener_task_id AND
 (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' ELSE p.listener_state END
 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id
 JOIN node_listener_states n ON n.node_id=op.agent_id WHERE p.deployment_id=? AND p.listener_task_id IS NOT NULL`, s.now().UTC().Format(time.RFC3339Nano), preparationID).Scan(&result.TaskID, &result.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &result, err
}

// Rebuild only the explicitly reviewed local listener. The normal publication
// projector is intentionally not used until dependent access is recovered.
func (s *Store) reinstallListenerTask(ctx context.Context, tx *sql.Tx, agentID, taskID string, buildingApproval bool) (*AgentTask, error) {
	var approved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_app_preparations WHERE listener_task_id=?)`, taskID).Scan(&approved); err != nil {
		return nil, err
	}
	if !approved {
		return nil, nil
	}
	var runtimeID, applicationID, expected string
	var approvalJSON, desiredJSON []byte
	var revision, attempt, currentRevision, currentAttempt int64
	err := tx.QueryRowContext(ctx, `SELECT p.runtime_command_id,p.application_id,p.approval_json,p.listener_revision,p.listener_attempt,p.listener_task_sha256,
 n.desired_revision,n.attempt,n.desired_json
 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id
 JOIN node_listener_states n ON n.node_id=op.agent_id JOIN application_commands c ON c.id=p.runtime_command_id
 WHERE p.listener_task_id=? AND op.agent_id=? AND c.state='succeeded'`, taskID, agentID).Scan(&runtimeID, &applicationID, &approvalJSON, &revision, &attempt, &expected, &currentRevision, &currentAttempt, &desiredJSON)
	if err != nil {
		return nil, errExecutionAuthorization
	}
	if revision < 1 || revision != currentRevision || attempt < 1 || currentAttempt < attempt-1 || currentAttempt > attempt || taskID != nodeListenerTaskID(agentID, revision) {
		return nil, errExecutionAuthorization
	}
	if runtime, err := s.reinstallRuntimeTask(ctx, tx, agentID, runtimeID, false); err != nil {
		return nil, err
	} else if runtime == nil {
		return nil, errExecutionAuthorization
	}
	var approval AgentReinstallNetworkApproval
	if json.Unmarshal(approvalJSON, &approval) != nil || !approval.Profile.DirectPublic {
		return nil, errors.New("center: approve the replacement public ingress before restoring its listener")
	}
	state, err := s.buildNodeListenerState(ctx, tx, agentID, revision, &nodeListenerRestoreTarget{ApplicationID: applicationID, BackendAddress: approval.Profile.ServiceAddress, BindAddress: approval.Profile.PublicBindAddress})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if string(encoded) != string(desiredJSON) {
		return nil, errors.New("center: reviewed listener configuration changed")
	}
	task := &AgentTask{Kind: "node.listener.apply", ID: taskID, Attempt: attempt, Revision: revision, NodeListenerState: &state}
	encoded, err = json.Marshal(task)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	if buildingApproval && expected != "" || !buildingApproval && (len(expected) != 64 || expected != hex.EncodeToString(digest[:])) {
		return nil, errExecutionAuthorization
	}
	return task, nil
}

func (s *Store) QueueAgentReinstallListener(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput) (AgentReinstallListener, error) {
	var result AgentReinstallListener
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var preparationID, runtimeID string
	err = tx.QueryRowContext(ctx, `SELECT p.deployment_id,p.runtime_command_id FROM agent_reinstall_app_preparations p
 JOIN agent_reinstall_operations op ON op.id=p.operation_id JOIN application_commands c ON c.id=p.runtime_command_id
 WHERE p.application_id=? AND op.id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required' AND c.state='succeeded'`, input.ApplicationID, input.OperationID, agentID, adminID).Scan(&preparationID, &runtimeID)
	if err != nil {
		return result, errExecutionAuthorization
	}
	if task, err := s.reinstallRuntimeTask(ctx, tx, agentID, runtimeID, false); err != nil {
		return result, err
	} else if task == nil {
		return result, errExecutionAuthorization
	}
	saved, err := s.readReinstallListener(ctx, tx, preparationID)
	if err != nil {
		return result, err
	}
	if saved != nil {
		if task, err := s.reinstallListenerTask(ctx, tx, agentID, saved.TaskID, false); err != nil {
			return result, err
		} else if task == nil {
			return result, errExecutionAuthorization
		}
		return *saved, nil
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if plan.Revision != input.PlanRevision || len(input.PlanRevision) != 64 {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return result, errors.New("center: settle previous work before restoring the entry")
	}
	if plan.NetworkReview == nil || !plan.NetworkReview.ApprovalCurrent || plan.NetworkReview.Approval == nil || !plan.NetworkReview.Approval.Profile.DirectPublic {
		return result, errors.New("center: approve the replacement public ingress before restoring its listener")
	}
	var revision, attempt int64
	err = tx.QueryRowContext(ctx, `SELECT desired_revision,attempt FROM node_listener_states WHERE node_id=?`, agentID).Scan(&revision, &attempt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if revision < 0 || revision == math.MaxInt64 || attempt < 0 || attempt == math.MaxInt64 {
		return result, errExecutionAuthorization
	}
	profile := plan.NetworkReview.Approval.Profile
	state, err := s.buildNodeListenerState(ctx, tx, agentID, revision+1, &nodeListenerRestoreTarget{ApplicationID: input.ApplicationID, BackendAddress: profile.ServiceAddress, BindAddress: profile.PublicBindAddress})
	if err != nil {
		return result, err
	}
	now := s.now().UTC()
	if err = s.saveNodeListenerState(ctx, tx, state, now, "Restore reviewed Meridian entry; public access verification remains pending"); err != nil {
		return result, err
	}
	id := nodeListenerTaskID(agentID, revision+1)
	update, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET listener_task_id=?,listener_revision=?,listener_attempt=?,listener_plan_revision=?,listener_state='pending' WHERE deployment_id=? AND listener_task_id IS NULL`, id, revision+1, attempt+1, input.PlanRevision, preparationID)
	if err != nil {
		return result, err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return result, errExecutionAuthorization
	}
	task, err := s.reinstallListenerTask(ctx, tx, agentID, id, true)
	if err != nil {
		return result, err
	}
	if task == nil {
		return result, errExecutionAuthorization
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(encoded)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET listener_task_sha256=? WHERE deployment_id=?`, hex.EncodeToString(digest[:]), preparationID); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, preparationID); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return AgentReinstallListener{TaskID: id, State: "pending"}, nil
}

func (s *Store) claimReinstallListener(ctx context.Context, tx *sql.Tx, agentID, id string, commitTask func(*sql.Tx, *AgentTask) error) (*AgentTask, error) {
	expected, err := s.reinstallListenerTask(ctx, tx, agentID, id, false)
	if err != nil {
		return nil, err
	}
	if expected == nil {
		return nil, errExecutionAuthorization
	}
	task, err := s.claimNodeListenerTask(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.ID != expected.ID || task.Attempt != expected.Attempt {
		return nil, errExecutionAuthorization
	}
	update, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET listener_state='running' WHERE listener_task_id=? AND listener_state='pending'`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return nil, errExecutionAuthorization
	}
	if err = s.recordReinstallListenerProgress(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = commitTask(tx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) recordReinstallListenerProgress(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=(SELECT operation_id FROM agent_reinstall_app_preparations WHERE listener_task_id=?)`, s.now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) completeReinstallListener(ctx context.Context, tx *sql.Tx, commit projectionCommit, agentID string, revision, attempt int64, succeeded bool, taskError string) error {
	id := nodeListenerTaskID(agentID, revision)
	task, err := s.reinstallListenerTask(ctx, tx, agentID, id, false)
	if err != nil {
		return err
	}
	if task == nil || task.Attempt != attempt {
		return errExecutionAuthorization
	}
	state, status := "failed", "failed"
	if succeeded {
		state, status = "succeeded", "ready"
		taskError = ""
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	update, err := tx.ExecContext(ctx, `UPDATE node_listener_states SET status=?,applied_revision=CASE WHEN ? THEN desired_revision ELSE applied_revision END,lease_expires_at='',last_error=?,updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status='applying'`, status, succeeded, taskError, now, agentID, revision, attempt)
	if err != nil {
		return err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	update, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET listener_state=? WHERE listener_task_id=? AND listener_state='running'`, state, id)
	if err != nil {
		return err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	if err = s.recordReinstallListenerProgress(ctx, tx, id); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, task.Kind, revision, state, "Entry listener receipt recorded; public access verification remains pending"); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleAgentReinstallListener(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallApplicationInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallListener(request.Context(), request.PathValue("id"), admin, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
