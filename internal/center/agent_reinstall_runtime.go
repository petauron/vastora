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
	"strings"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type AgentReinstallRuntime struct {
	CommandID string `json:"commandId"`
	State     string `json:"state"`
}

func (s *Store) readReinstallRuntime(ctx context.Context, tx *sql.Tx, preparationID string) (*AgentReinstallRuntime, error) {
	var result AgentReinstallRuntime
	err := tx.QueryRowContext(ctx, `SELECT c.id,CASE WHEN c.state='running' AND (c.lease_expires_at<=? OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=c.id AND (e.state IN ('unknown','failed') OR e.phase='result_received'))) THEN 'needs_review' WHEN c.state='succeeded' AND COALESCE(json_extract(c.result_json,'$.transportReady'),0)<>1 THEN 'needs_review' ELSE c.state END
 FROM agent_reinstall_app_preparations p JOIN application_commands c ON c.id=p.runtime_command_id WHERE p.deployment_id=?`, s.now().UTC().Format(time.RFC3339Nano), preparationID).Scan(&result.CommandID, &result.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &result, err
}

// Return the exact approved task, freshly built from retained credentials. Its
// digest is the durable authorization; secret-bearing JSON stays in execution
// encryption and is never stored in recovery metadata or returned to the UI.
func (s *Store) reinstallRuntimeTask(ctx context.Context, tx *sql.Tx, agentID, commandID string, buildingApproval bool) (*AgentTask, error) {
	var preparationID, endpointID, expected string
	var revision uint64
	var input, packageJSON []byte
	var attempt int64
	var completedWithoutTransport bool
	err := tx.QueryRowContext(ctx, `SELECT p.deployment_id,p.runtime_endpoint_id,p.runtime_revision,p.runtime_task_sha256,p.task_json,c.input_json,c.attempt,c.state='succeeded' AND COALESCE(json_extract(c.result_json,'$.transportReady'),0)<>1
 FROM agent_reinstall_app_preparations p JOIN deployments d ON d.id=p.deployment_id JOIN application_commands c ON c.id=p.runtime_command_id
 WHERE c.id=? AND d.agent_id=? AND c.agent_id=d.agent_id AND c.gateway_node_id=d.agent_id AND c.application_id=p.application_id
 AND d.state='succeeded' AND c.kind=? AND c.reconciliation_requested=0 AND c.reconciliation_required=0`, commandID, agentID, meridianruntime.ApplyKind).Scan(&preparationID, &endpointID, &revision, &expected, &packageJSON, &input, &attempt, &completedWithoutTransport)
	if errors.Is(err, sql.ErrNoRows) && !strings.HasPrefix(commandID, "reinstall-runtime-") {
		return nil, nil
	}
	if err != nil {
		return nil, errExecutionAuthorization
	}
	if completedWithoutTransport {
		return nil, errors.New("center: restored landing transport requires inspection before activating access")
	}
	if valid, err := s.validateReinstallPreparation(ctx, tx, agentID, preparationID); err != nil {
		return nil, err
	} else if !valid {
		return nil, errExecutionAuthorization
	}
	var command meridianruntime.Command
	if json.Unmarshal(input, &command) != nil || command.EndpointID != endpointID || command.ReplacePendingState || attempt > 1 || revision == 0 {
		return nil, errExecutionAuthorization
	}
	var prepared AgentTask
	if json.Unmarshal(packageJSON, &prepared) != nil {
		return nil, errExecutionAuthorization
	}
	image := ""
	for _, entry := range prepared.Manifest.Images {
		if entry.Name == "xray-core" {
			image = entry.Reference
		}
	}
	target := &meridianRuntimeRestoreTarget{Address: prepared.ServiceAddress, Image: image, Revision: revision}
	landing, err := s.readReinstallLandingSource(ctx, tx, agentID, preparationID)
	if err != nil {
		return nil, err
	}
	if landing != nil {
		if landing.State != "authorized" {
			return nil, errors.New("center: reviewed landing authorization changed")
		}
		target.RestoreLandings = true
	}
	projection, err := s.buildMeridianRuntimeProjection(ctx, tx, endpointID, agentID, target)
	if err != nil {
		return nil, err
	}
	if projection.task.ApplicationID != prepared.ApplicationID {
		return nil, errExecutionAuthorization
	}
	task := &AgentTask{Kind: "application.command", ID: commandID, Attempt: 1, Revision: int64(revision), MeridianRuntime: &projection.task}
	encoded, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	if buildingApproval && expected != "" || !buildingApproval && (len(expected) != 64 || expected != hex.EncodeToString(digest[:])) {
		return nil, errors.New("center: reviewed runtime configuration changed")
	}
	return task, nil
}

func (s *Store) QueueAgentReinstallRuntime(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput) (AgentReinstallRuntime, error) {
	var result AgentReinstallRuntime
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var preparationID string
	err = tx.QueryRowContext(ctx, `SELECT p.deployment_id FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id
 WHERE p.application_id=? AND op.id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required'`, input.ApplicationID, input.OperationID, agentID, adminID).Scan(&preparationID)
	if err != nil {
		return result, errExecutionAuthorization
	}
	if _, err = s.validateReinstallPreparation(ctx, tx, agentID, preparationID); err != nil {
		return result, err
	}
	saved, err := s.readReinstallRuntime(ctx, tx, preparationID)
	if err != nil {
		return result, err
	}
	if saved != nil {
		if _, err = s.reinstallRuntimeTask(ctx, tx, agentID, saved.CommandID, false); err != nil {
			return result, err
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
		return result, errors.New("center: settle previous work before runtime restoration")
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM deployments WHERE id=?`, preparationID).Scan(&state); err != nil {
		return result, err
	}
	if state != "succeeded" {
		return result, errors.New("center: prepare the saved Meridian package before restoring runtime")
	}
	landing, err := s.readReinstallLandingSource(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	if landing != nil && landing.State != "authorized" {
		return result, errors.New("center: finish landing identity replacement before restoring runtime")
	}
	var endpointID string
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT id,desired_revision FROM meridian_endpoints WHERE application_id=? AND status<>'retired'`, input.ApplicationID).Scan(&endpointID, &revision); err != nil {
		return result, errors.New("center: saved Meridian endpoint is unavailable")
	}
	if revision < 1 || revision >= math.MaxInt64 {
		return result, errors.New("center: invalid saved Meridian revision")
	}
	// Keep the reviewed source pin and reserve the runtime after landing receipts.
	// Reserving a fresh revision invalidates old runtime/publication readiness.
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',updated_at=? WHERE id=?`, now, endpointID); err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id = "reinstall-runtime-" + id
	encoded, _ := json.Marshal(meridianruntime.Command{EndpointID: endpointID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, input.ApplicationID, agentID, agentID, meridianruntime.ApplyKind, encoded, now, now); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET runtime_command_id=?,runtime_endpoint_id=?,runtime_plan_revision=?,runtime_revision=? WHERE deployment_id=? AND runtime_command_id IS NULL`, id, endpointID, input.PlanRevision, revision+1, preparationID); err != nil {
		return result, err
	}
	task, err := s.reinstallRuntimeTask(ctx, tx, agentID, id, true)
	if err != nil {
		return result, err
	}
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(taskJSON)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET runtime_task_sha256=? WHERE deployment_id=?`, hex.EncodeToString(digest[:]), preparationID); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, preparationID); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, "application.command", revision+1, "queued", "Restore reviewed Meridian runtime; entry and client verification remain pending"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return AgentReinstallRuntime{CommandID: id, State: "pending"}, nil
}

func (s *Store) claimReinstallRuntime(ctx context.Context, tx *sql.Tx, agentID, id string, commitTask func(*sql.Tx, *AgentTask) error) (*AgentTask, error) {
	task, err := s.reinstallRuntimeTask(ctx, tx, agentID, id, false)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errExecutionAuthorization
	}
	now := s.now().UTC()
	update, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=1,lease_expires_at=?,updated_at=? WHERE id=? AND state='pending' AND attempt=0`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return nil, errExecutionAuthorization
	}
	if err = s.recordReinstallRuntimeProgress(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, task.Kind, task.Revision, "claimed", "Restore approved runtime"); err != nil {
		return nil, err
	}
	if err = commitTask(tx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) recordReinstallRuntimeProgress(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=(SELECT operation_id FROM agent_reinstall_app_preparations WHERE runtime_command_id=?)`, s.now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) completeReinstallRuntime(ctx context.Context, tx *sql.Tx, commit projectionCommit, agentID, id string, succeeded bool, taskError string, raw json.RawMessage) error {
	task, err := s.reinstallRuntimeTask(ctx, tx, agentID, id, false)
	if err != nil {
		return err
	}
	if task == nil {
		return errExecutionAuthorization
	}
	resultJSON := []byte(`{}`)
	state := "failed"
	if succeeded {
		var envelope ApplicationTaskResult
		if json.Unmarshal(raw, &envelope) != nil || envelope.MeridianRuntime == nil || envelope.MeridianRuntime.LegacyRetired || len(envelope.GeneratedSecrets) != 0 || envelope.MeridianRuntime.Validate(task.MeridianRuntime.Desired) != nil {
			return errors.New("center: invalid restored Meridian runtime receipt")
		}
		health, err := envelope.MeridianRuntime.PeerHealth(*task.MeridianRuntime, s.now().UTC())
		if err != nil {
			return err
		}
		// Validate retained usage evidence without inventing a reset or changing the
		// account ledger during isolated runtime preparation.
		var endpointID string
		if err = tx.QueryRowContext(ctx, `SELECT runtime_endpoint_id FROM agent_reinstall_app_preparations WHERE runtime_command_id=?`, id).Scan(&endpointID); err != nil {
			return err
		}
		materials, _, err := s.meridianRuntimeMaterials(ctx, tx, endpointID, task.MeridianRuntime.ApplicationID)
		if err != nil {
			return err
		}
		if _, err = meridian.ParseXrayUserCounters(materials, envelope.MeridianRuntime.Stats); err != nil {
			return err
		}
		transportReady := true
		for _, healthy := range health {
			if !healthy {
				transportReady = false
			}
		}
		resultJSON, _ = json.Marshal(struct {
			Runtime        *meridianruntime.Result `json:"runtime"`
			TransportReady bool                    `json:"transportReady"`
		}{envelope.MeridianRuntime, transportReady})
		// Execution success records the applied configuration. Transport is
		// independent evidence: a negative result blocks recovery progression
		// without rewriting the successful execution as a failed side effect.
		state, taskError = "succeeded", ""

	}
	updated, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,error=?,lease_expires_at='',updated_at=? WHERE id=? AND state='running' AND attempt=1`, state, resultJSON, taskError, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	if succeeded {
		var envelope ApplicationTaskResult
		if json.Unmarshal(raw, &envelope) != nil || envelope.MeridianRuntime == nil {
			return errExecutionAuthorization
		}
		if err = s.recordReinstallRuntimeObservation(ctx, tx, agentID, *envelope.MeridianRuntime, s.now().UTC()); err != nil {
			return err
		}
	}
	if err = s.recordReinstallRuntimeProgress(ctx, tx, id); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, task.Kind, task.Revision, state, "Runtime restoration receipt recorded; client verification remains pending"); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleAgentReinstallRuntime(writer http.ResponseWriter, request *http.Request) {
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
	result, err := s.store.QueueAgentReinstallRuntime(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
