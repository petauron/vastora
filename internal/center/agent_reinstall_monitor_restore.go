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
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

type AgentReinstallMonitorRestore struct {
	DeploymentID string `json:"deploymentId"`
	State        string `json:"state"`
}

func (s *Store) readReinstallMonitorRestore(ctx context.Context, tx *sql.Tx, rotationID string) (*AgentReinstallMonitorRestore, error) {
	var value AgentReinstallMonitorRestore
	var agentID string
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.agent_id,CASE WHEN d.state='running' AND (d.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=d.id AND (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' ELSE d.state END
 FROM agent_reinstall_monitor_restorations m JOIN deployments d ON d.id=m.deployment_id WHERE m.rotation_command_id=?`, s.now().UTC().Format(time.RFC3339Nano), rotationID).Scan(&value.DeploymentID, &agentID, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = s.reinstallMonitorRestoreTask(ctx, tx, agentID, value.DeploymentID, false); err != nil {
		value.State = "needs_review"
	}
	return &value, nil
}

// Recover the exact rotated secret from authenticated encrypted execution
// evidence. A metadata receipt, edited node ID or plaintext command result is
// never sufficient to install a credential on the replacement machine.
func (s *Store) reinstallRotatedCredentials(ctx context.Context, tx *sql.Tx, serviceAgentID, commandID string, expected pulse.RotationTask) (pulse.RestoreCredentials, error) {
	var credentials pulse.RestoreCredentials
	var id, digest string
	var sealedTask, sealedResult []byte
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.digest,e.sealed_task,e.sealed_result FROM task_executions e JOIN application_commands c ON c.id=e.task_id
 WHERE c.id=? AND c.agent_id=? AND c.state='succeeded' AND e.agent_id=c.agent_id AND e.attempt=1 AND e.kind='application.command' AND e.state='succeeded' AND e.phase='reported'`, commandID, serviceAgentID).Scan(&id, &digest, &sealedTask, &sealedResult)
	if err != nil {
		return credentials, errExecutionAuthorization
	}
	raw, err := secret.Open(s.key, sealedTask, []byte("execution-task:"+id))
	if err != nil {
		return credentials, errExecutionAuthorization
	}
	sum := sha256.Sum256(raw)
	var task AgentTask
	if hex.EncodeToString(sum[:]) != digest || json.Unmarshal(raw, &task) != nil || task.ID != commandID || task.Attempt != 1 || task.Kind != "application.command" || task.PulseRotation == nil {
		return credentials, errExecutionAuthorization
	}
	a, _ := json.Marshal(task.PulseRotation)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		return credentials, errExecutionAuthorization
	}
	raw, err = secret.Open(s.key, sealedResult, []byte("execution-result:"+id))
	if err != nil {
		return credentials, errExecutionAuthorization
	}
	var evidence executionResultEvidence
	var result struct {
		Rotation *pulse.RotationResult `json:"pulseRotation"`
	}
	if json.Unmarshal(raw, &evidence) != nil || !evidence.Succeeded || evidence.Unknown || json.Unmarshal(evidence.Result, &result) != nil || result.Rotation == nil || result.Rotation.Validate(expected) != nil {
		return credentials, errExecutionAuthorization
	}
	credentials = pulse.RestoreCredentials{NodeID: result.Rotation.NodeID, Token: result.Rotation.Token}
	if credentials.Validate() != nil {
		return pulse.RestoreCredentials{}, errExecutionAuthorization
	}
	return credentials, nil
}

func (s *Store) QueueAgentReinstallMonitorRestore(ctx context.Context, agentID, adminID string, input AgentReinstallMonitorInput) (AgentReinstallMonitorRestore, error) {
	var result AgentReinstallMonitorRestore
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var rotationID, serviceAgentID string
	err = tx.QueryRowContext(ctx, `SELECT m.command_id,c.agent_id FROM agent_reinstall_monitor_rotations m JOIN application_commands c ON c.id=m.command_id JOIN agent_reinstall_operations op ON op.id=m.operation_id
 WHERE m.operation_id=? AND m.application_id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required' AND c.state='succeeded' AND json_extract(m.result_json,'$.state')='rotated'`, input.OperationID, input.ApplicationID, agentID, adminID).Scan(&rotationID, &serviceAgentID)
	if err != nil {
		return result, errExecutionAuthorization
	}
	rotation, err := s.validateReinstallMonitorRotation(ctx, tx, serviceAgentID, rotationID)
	if err != nil {
		return result, err
	}
	previous, err := s.readReinstallMonitorRestore(ctx, tx, rotationID)
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
	if len(input.PlanRevision) != 64 || plan.Revision != input.PlanRevision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) > 0 || len(plan.UnclaimedLocalWork) > 0 {
		return result, errors.New("center: settle previous machine work before restoring monitoring")
	}
	if plan.NetworkReview == nil || plan.NetworkReview.Approval == nil || !plan.NetworkReview.ApprovalCurrent {
		return result, errors.New("center: approve the replacement network before restoring monitoring")
	}
	index := slices.IndexFunc(plan.Applications, func(app AgentReinstallApplication) bool {
		return app.ApplicationID == input.ApplicationID && app.AppKey == pulseAgentAppKey && app.Recovery == "reenroll_monitor"
	})
	if index < 0 {
		return result, errExecutionAuthorization
	}
	var source AgentTask
	var version string
	var manifest, config []byte
	var registry sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT d.app_key,d.app_version,d.manifest_json,d.config_json,d.registry_credential_id,a.role FROM deployments d JOIN applications a ON a.id=d.application_id WHERE d.id=? AND d.application_id=? AND d.agent_id=? AND d.operation IN ('install','upgrade','configure') AND d.reconciliation_required=0`, plan.Applications[index].DeploymentID, input.ApplicationID, agentID).Scan(&source.AppKey, &version, &manifest, &config, &registry, &source.ApplicationRole)
	if err != nil {
		return result, err
	}
	var pulseConfig pulse.AgentConfig
	if json.Unmarshal(manifest, &source.Manifest) != nil || source.Manifest.ID != "pulse-agent" || source.Manifest.Version != version || agent.ValidateOfficialContract(source.Manifest) != nil || registry.Valid || json.Unmarshal(config, &pulseConfig) != nil || pulseConfig.Validate() != nil || pulseConfig.ServiceApplicationID != rotation.Task.Inspection.ApplicationID {
		return result, errors.New("center: saved Pulse package or service configuration requires review")
	}
	credentials, err := s.reinstallRotatedCredentials(ctx, tx, serviceAgentID, rotationID, rotation.Task)
	if err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id = "reinstall-monitor-collector-" + id
	rawSecret, _ := json.Marshal(credentials)
	secretID, err := s.putSecret(ctx, tx, rawSecret, "deployment:"+id)
	if err != nil {
		return result, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,secret_id,operation,state,created_at,updated_at,application_id,service_address,runtime_generation,pre_dispatch_application_status)
 VALUES(?,?,?,?,?,?,?,'install','pending',?,?,?,'',?,(SELECT status FROM applications WHERE id=?))`, id, agentID, source.AppKey, version, manifest, config, secretID, now, now, input.ApplicationID, platform.ApplicationRuntimeGeneration, input.ApplicationID)
	if err != nil {
		return result, err
	}
	approval, _ := json.Marshal(plan.NetworkReview.Approval)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_monitor_restorations(deployment_id,rotation_command_id,source_deployment_id,plan_revision,approval_json,task_sha256) VALUES(?,?,?,?,?,'')`, id, rotationID, plan.Applications[index].DeploymentID, input.PlanRevision, approval)
	if err != nil {
		return result, err
	}
	task, err := s.reinstallMonitorRestoreTask(ctx, tx, agentID, id, true)
	if err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(task)
	sum := sha256.Sum256(encoded)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_monitor_restorations SET task_sha256=? WHERE deployment_id=?`, hex.EncodeToString(sum[:]), id); err != nil {
		return result, err
	}
	if err = s.recordReinstallMonitorRestoreProgress(ctx, tx, id); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, "application.apply", applicationTaskRevision, "queued", "Restore the saved Pulse collector with the original monitoring identity; reporting still requires verification"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return AgentReinstallMonitorRestore{DeploymentID: id, State: "pending"}, nil
}

func (s *Store) reinstallMonitorRestoreTask(ctx context.Context, tx *sql.Tx, agentID, taskID string, building bool) (*AgentTask, error) {
	var task AgentTask
	var rotationID, serviceAgentID, expected, version string
	var approval, manifest, config, sealed []byte
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT m.rotation_command_id,c.agent_id,m.task_sha256,m.approval_json,d.app_key,d.app_version,d.manifest_json,d.config_json,d.application_id,a.role,sealed.sealed,d.attempt
 FROM agent_reinstall_monitor_restorations m JOIN deployments d ON d.id=m.deployment_id JOIN application_commands c ON c.id=m.rotation_command_id
 JOIN agent_reinstall_monitor_rotations r ON r.command_id=c.id JOIN applications a ON a.id=d.application_id JOIN agents n ON n.id=d.agent_id JOIN secrets sealed ON sealed.id=d.secret_id
 WHERE d.id=? AND d.agent_id=? AND a.node_id=d.agent_id AND d.app_key=? AND a.app_key=d.app_key AND d.application_id=r.application_id
 AND d.operation='install' AND d.delete_data=0 AND d.registry_credential_id IS NULL AND d.service_address='' AND d.runtime_generation=? AND d.reconciliation_required=0 AND d.reconciliation_requested=0
 AND (d.state<>'pending' OR d.attempt=0) AND json_extract(n.capabilities_json,'$.pulseRestore')=1`, taskID, agentID, pulseAgentAppKey, platform.ApplicationRuntimeGeneration).Scan(&rotationID, &serviceAgentID, &expected, &approval, &task.AppKey, &version, &manifest, &config, &task.ApplicationID, &task.ApplicationRole, &sealed, &attempt)
	if errors.Is(err, sql.ErrNoRows) && !strings.HasPrefix(taskID, "reinstall-monitor-collector-") {
		return nil, nil
	}
	if err != nil || attempt > 1 {
		return nil, errExecutionAuthorization
	}
	rotation, err := s.validateReinstallMonitorRotation(ctx, tx, serviceAgentID, rotationID)
	if err != nil {
		return nil, err
	}
	credentials, err := s.reinstallRotatedCredentials(ctx, tx, serviceAgentID, rotationID, rotation.Task)
	if err != nil {
		return nil, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("deployment:"+taskID))
	if err != nil {
		return nil, errExecutionAuthorization
	}
	var stored pulse.RestoreCredentials
	if json.Unmarshal(raw, &stored) != nil || stored != credentials {
		return nil, errExecutionAuthorization
	}
	network, err := s.agentReinstallNetworkReview(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if network == nil || !network.ApprovalCurrent || network.Approval == nil {
		return nil, errExecutionAuthorization
	}
	current, _ := json.Marshal(network.Approval)
	if string(current) != string(approval) {
		return nil, errExecutionAuthorization
	}
	if json.Unmarshal(manifest, &task.Manifest) != nil || task.Manifest.ID != "pulse-agent" || task.Manifest.Version != version || agent.ValidateOfficialContract(task.Manifest) != nil {
		return nil, errExecutionAuthorization
	}
	task.Kind = "application.apply"
	task.ID = taskID
	task.Attempt = 1
	task.Revision = applicationTaskRevision
	task.Operation = "install"
	task.Config = config
	task.Secrets = json.RawMessage(`{}`)
	task.RequiredRuntimeGeneration = platform.ApplicationRuntimeGeneration
	task.PulseRestore = &credentials
	encoded, _ := json.Marshal(task)
	sum := sha256.Sum256(encoded)
	if building && expected != "" || !building && expected != hex.EncodeToString(sum[:]) {
		return nil, errExecutionAuthorization
	}
	return &task, nil
}

func (s *Store) recordReinstallMonitorRestoreProgress(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=(SELECT r.operation_id FROM agent_reinstall_monitor_restorations m JOIN agent_reinstall_monitor_rotations r ON r.command_id=m.rotation_command_id WHERE m.deployment_id=?)`, s.now().UTC().Format(time.RFC3339Nano), taskID)
	return err
}

func (s *Server) handleAgentReinstallMonitorRestore(writer http.ResponseWriter, request *http.Request) {
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
	result, err := s.store.QueueAgentReinstallMonitorRestore(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
