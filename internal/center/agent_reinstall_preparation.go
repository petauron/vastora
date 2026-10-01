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
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

type AgentReinstallApplicationInput struct {
	OperationID   string `json:"operationId"`
	PlanRevision  string `json:"planRevision"`
	ApplicationID string `json:"applicationId"`
}

type AgentReinstallPreparation struct {
	DeploymentID string                 `json:"deploymentId"`
	State        string                 `json:"state"`
	Runtime      *AgentReinstallRuntime `json:"runtime,omitempty"`
}

func (s *Store) readReinstallPreparation(ctx context.Context, tx *sql.Tx, agentID, applicationID string) (*AgentReinstallPreparation, error) {
	var value AgentReinstallPreparation
	err := tx.QueryRowContext(ctx, `SELECT d.id,CASE WHEN d.state='running' AND (d.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=d.id AND (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' ELSE d.state END FROM agent_reinstall_app_preparations p JOIN deployments d ON d.id=p.deployment_id
 JOIN agent_reinstall_operations op ON op.id=p.operation_id WHERE op.agent_id=? AND p.application_id=? AND op.state NOT IN ('completed','superseded')`, s.now().UTC().Format(time.RFC3339Nano), agentID, applicationID).Scan(&value.DeploymentID, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value.Runtime, err = s.readReinstallRuntime(ctx, tx, value.DeploymentID)
	return &value, err
}

// This is an approved package preparation, not a replay of the old deployment.
// Meridian's existing installer pulls its audited image without creating a
// runtime. The recovery fence and original business state remain in place.
func (s *Store) QueueAgentReinstallPreparation(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput) (AgentReinstallPreparation, error) {
	var result AgentReinstallPreparation
	if input.OperationID == "" || len(input.PlanRevision) != 64 || input.ApplicationID == "" {
		return result, errors.New("center: review the application recovery plan before preparation")
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
	err = tx.QueryRowContext(ctx, `SELECT x25519_public_key,status='active' AND credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=?) FROM agents WHERE id=?`, adminID, agentID).Scan(&key, &active)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(key)
	if !active || hex.EncodeToString(sum[:]) != op.ReplacementFingerprint {
		return result, errExecutionAuthorization
	}
	previous, err := s.readReinstallPreparation(ctx, tx, agentID, input.ApplicationID)
	if err != nil {
		return result, err
	}
	if previous != nil {
		if _, err = s.validateReinstallPreparation(ctx, tx, agentID, previous.DeploymentID); err != nil {
			return result, err
		}
		return *previous, nil
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if plan.Revision != input.PlanRevision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) > 0 || len(plan.UnclaimedLocalWork) > 0 {
		return result, errors.New("center: settle previous machine work before preparing applications")
	}
	if plan.NetworkReview == nil || !plan.NetworkReview.ApprovalCurrent || plan.NetworkReview.Approval == nil {
		return result, errors.New("center: approve the replacement network before preparing applications")
	}
	index := slices.IndexFunc(plan.Applications, func(app AgentReinstallApplication) bool { return app.ApplicationID == input.ApplicationID })
	if index < 0 || plan.Applications[index].AppKey != meridianAppKey || plan.Applications[index].Recovery != "rebuild_configuration" {
		return result, errors.New("center: only a reviewed Meridian package can be prepared")
	}
	source := plan.Applications[index]
	var sourceTask AgentTask
	var manifest, config []byte
	var secretID, registryID sql.NullString
	var version string
	err = tx.QueryRowContext(ctx, `SELECT d.app_key,d.app_version,d.manifest_json,d.config_json,d.secret_id,d.registry_credential_id,a.role
 FROM deployments d JOIN applications a ON a.id=d.application_id WHERE d.id=? AND d.application_id=? AND d.agent_id=? AND d.operation IN ('install','upgrade','configure') AND d.reconciliation_required=0`, source.DeploymentID, input.ApplicationID, agentID).Scan(&sourceTask.AppKey, &version, &manifest, &config, &secretID, &registryID, &sourceTask.ApplicationRole)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(manifest, &sourceTask.Manifest) != nil || agent.ValidateOfficialContract(sourceTask.Manifest) != nil || sourceTask.Manifest.ID != "meridian" || sourceTask.Manifest.Version != version || secretID.Valid || registryID.Valid || string(config) != "{}" {
		return result, errors.New("center: saved Meridian package or configuration requires review")
	}
	address := plan.NetworkReview.Approval.Profile.ServiceAddress
	if !networking.IsPrivateServiceAddress(address) {
		return result, errors.New("center: Meridian requires an approved private service address")
	}
	sourcePlan := AgentReinstallPlan{AgentID: agentID}
	sourceRevision, err := readReinstallApplications(ctx, tx, &sourcePlan)
	if err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id = "reinstall-package-" + id
	sourceTask.Kind = "application.apply"
	sourceTask.ID = id
	sourceTask.Attempt = 1
	sourceTask.Revision = applicationTaskRevision
	sourceTask.Config = json.RawMessage(`{}`)
	sourceTask.Secrets = json.RawMessage(`{}`)
	sourceTask.Operation = "install"
	sourceTask.ApplicationID = input.ApplicationID
	sourceTask.ServiceAddress = address
	sourceTask.RequiredRuntimeGeneration = platform.ApplicationRuntimeGeneration
	taskJSON, err := json.Marshal(sourceTask)
	if err != nil {
		return result, err
	}
	approvalJSON, err := json.Marshal(plan.NetworkReview.Approval)
	if err != nil {
		return result, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id,service_address,runtime_generation,pre_dispatch_application_status)
 VALUES(?,?,?,?,?,?,'install','pending',?,?,?,?,?,(SELECT status FROM applications WHERE id=?))`, id, agentID, meridianAppKey, version, manifest, config, now, now, input.ApplicationID, address, platform.ApplicationRuntimeGeneration, input.ApplicationID)
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_app_preparations(deployment_id,source_deployment_id,operation_id,application_id,plan_revision,source_revision,replacement_key,approval_json,task_json) VALUES(?,?,?,?,?,?,?,?,?)`, id, source.DeploymentID, op.ID, input.ApplicationID, input.PlanRevision, sourceRevision, key, approvalJSON, taskJSON)
	if err != nil {
		return result, err
	}
	if _, err = s.validateReinstallPreparation(ctx, tx, agentID, id); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, id); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, "application.apply", applicationTaskRevision, "queued", "Prepare the reviewed Meridian package; business restoration remains pending"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return AgentReinstallPreparation{DeploymentID: id, State: "pending"}, nil
}

// Recovery sheets refresh when the operation changes, including task progress.
func (s *Store) recordReinstallPreparationProgress(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=(SELECT operation_id FROM agent_reinstall_app_preparations WHERE deployment_id=?)`, s.now().UTC().Format(time.RFC3339Nano), taskID)
	return err
}

// Called again at selection, execution authorization and result projection.
// A receipt ID alone never authorizes changed intent or a different machine.
func (s *Store) validateReinstallPreparation(ctx context.Context, tx *sql.Tx, agentID, taskID string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_reinstall_app_preparations WHERE deployment_id=?`, taskID).Scan(&count); err != nil {
		return false, err
	}
	if count == 0 {
		if strings.HasPrefix(taskID, "reinstall-package-") {
			return true, errExecutionAuthorization
		}
		return false, nil
	}
	var sourceRevision string
	var approvalJSON, expected []byte
	var task AgentTask
	var manifest, config []byte
	var secretID, registryID sql.NullString
	var version string
	var attempt int64
	var reconciliation bool
	err := tx.QueryRowContext(ctx, `SELECT p.source_revision,p.approval_json,p.task_json,d.id,d.app_key,d.app_version,d.manifest_json,d.config_json,d.operation,d.application_id,a.role,d.service_address,d.runtime_generation,d.delete_data,d.secret_id,d.registry_credential_id,d.attempt,d.reconciliation_required OR d.reconciliation_requested
 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id JOIN deployments d ON d.id=p.deployment_id
 JOIN applications a ON a.id=d.application_id JOIN agents n ON n.id=op.agent_id
 WHERE p.deployment_id=? AND d.agent_id=? AND op.agent_id=d.agent_id AND a.node_id=d.agent_id AND p.application_id=a.id
 AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required') AND op.replacement_fingerprint<>''
 AND n.status='active' AND n.credential_revoked_at='' AND n.x25519_public_key=p.replacement_key
 AND json_extract(n.capabilities_json,'$.docker')=1 AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by)`, taskID, agentID).Scan(&sourceRevision, &approvalJSON, &expected, &task.ID, &task.AppKey, &version, &manifest, &config, &task.Operation, &task.ApplicationID, &task.ApplicationRole, &task.ServiceAddress, &task.RequiredRuntimeGeneration, &task.DeleteData, &secretID, &registryID, &attempt, &reconciliation)
	if err != nil {
		return true, errExecutionAuthorization
	}
	if json.Unmarshal(manifest, &task.Manifest) != nil || agent.ValidateOfficialContract(task.Manifest) != nil || task.Manifest.Version != version || secretID.Valid || registryID.Valid || attempt > 1 || reconciliation {
		return true, errExecutionAuthorization
	}
	task.Kind = "application.apply"
	task.Attempt = 1
	task.Revision = applicationTaskRevision
	task.Config = config
	task.Secrets = json.RawMessage(`{}`)
	actual, err := json.Marshal(task)
	if err != nil {
		return true, err
	}
	if string(actual) != string(expected) {
		return true, errExecutionAuthorization
	}
	plan := AgentReinstallPlan{AgentID: agentID}
	current, err := readReinstallApplications(ctx, tx, &plan)
	if err != nil {
		return true, err
	}
	if current != sourceRevision {
		return true, errors.New("center: saved application intent changed; review recovery again")
	}
	review, err := s.agentReinstallNetworkReview(ctx, tx, agentID)
	if err != nil {
		return true, err
	}
	if review == nil || !review.ApprovalCurrent || review.Approval == nil {
		return true, errExecutionAuthorization
	}
	currentApproval, err := json.Marshal(review.Approval)
	if err != nil {
		return true, err
	}
	if string(currentApproval) != string(approvalJSON) {
		return true, errExecutionAuthorization
	}
	return true, nil
}

func (s *Store) claimAgentReinstallTask(ctx context.Context, agentID, credential, requiredTaskID string, commitTask func(*sql.Tx, *AgentTask) error) (*AgentTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return nil, err
	} else if paused {
		return nil, errExecutionBlocked
	}
	var generation int
	var version string
	err = tx.QueryRowContext(ctx, `SELECT runtime_generation,version FROM agents WHERE id=? AND status='active' AND credential_revoked_at='' AND credential_hash=?`, agentID, tokenHash(credential)).Scan(&generation, &version)
	if err != nil {
		return nil, errExecutionAuthorization
	}
	if agentVersionBehindTarget(version, Version) {
		return nil, errExecutionBlocked
	}
	if blocked, err := unresolvedExecutionBlocksAgentWork(ctx, tx, agentID, version); err != nil {
		return nil, err
	} else if blocked {
		return nil, errExecutionBlocked
	}
	id, err := pendingAgentReinstallTask(ctx, tx, agentID, requiredTaskID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(id, "reinstall-runtime-") {
		return s.claimReinstallRuntime(ctx, tx, agentID, id, commitTask)
	}
	pending, err := readPendingApplicationDeployment(ctx, tx, agentID, id)
	if err != nil {
		return nil, err
	}
	return s.claimApplicationDeployment(ctx, tx, agentID, generation, pending, commitTask)
}

func (s *Server) handleAgentReinstallPreparation(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallApplicationInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallPreparation(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}

func pendingAgentReinstallTask(ctx context.Context, tx *sql.Tx, agentID, requiredTaskID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM (
 SELECT d.id,d.created_at FROM agent_reinstall_app_preparations p JOIN deployments d ON d.id=p.deployment_id JOIN agent_reinstall_operations op ON op.id=p.operation_id
 WHERE d.agent_id=? AND op.agent_id=d.agent_id AND op.state='review_required' AND d.state='pending' AND d.attempt=0
 UNION ALL SELECT c.id,c.created_at FROM agent_reinstall_app_preparations p JOIN application_commands c ON c.id=p.runtime_command_id JOIN agent_reinstall_operations op ON op.id=p.operation_id
 WHERE c.agent_id=? AND op.agent_id=c.agent_id AND op.state='review_required' AND c.state='pending' AND c.attempt=0
 ) WHERE (?='' OR id=?) ORDER BY created_at,id LIMIT 1`, agentID, agentID, requiredTaskID, requiredTaskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errExecutionBlocked
	}
	return id, err
}
