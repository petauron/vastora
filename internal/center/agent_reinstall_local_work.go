package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

type AgentReinstallLocalWorkInput struct {
	OperationID  string `json:"operationId"`
	PlanRevision string `json:"planRevision"`
	ConfirmLocal bool   `json:"confirmLocal"`
}

type AgentReinstallLocalDisposition struct {
	PlanRevision string    `json:"planRevision"`
	ExecutionIDs []string  `json:"executionIds"`
	AuthorizedBy string    `json:"authorizedBy"`
	DisposedAt   time.Time `json:"disposedAt"`
}

func readReinstallLocalDisposition(ctx context.Context, tx *sql.Tx, agentID string) (*AgentReinstallLocalDisposition, error) {
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT d.disposition_json FROM agent_reinstall_local_dispositions d
	 JOIN agent_reinstall_operations r ON r.id=d.operation_id WHERE r.agent_id=? AND r.state NOT IN ('superseded','completed')
	 ORDER BY d.rowid DESC LIMIT 1`, agentID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result AgentReinstallLocalDisposition
	return &result, json.Unmarshal(encoded, &result)
}

// Only known host-local effects can be settled from old-machine isolation.
// Monitoring enrollment, arbitrary applications, gateway certificate operations
// and external tunnel changes need their own evidence, even on the same host.
func (s *Store) reinstallLocalExecutionTask(ctx context.Context, tx *sql.Tx, execution AgentReinstallExecution) (*AgentTask, error) {
	if !execution.IdentityRetired || (execution.State != "failed" && execution.State != "unknown") {
		return nil, nil
	}
	var sealed []byte
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT sealed_task,digest FROM task_executions
	 WHERE id=? AND agent_id=? AND task_id=? AND kind=? AND attempt=? AND identity_retired_at<>'' AND disposition=''`,
		execution.ID, execution.AgentID, execution.TaskID, execution.Kind, execution.Attempt).Scan(&sealed, &digest)
	if err != nil {
		return nil, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+execution.ID))
	if err != nil {
		return nil, nil
	}
	sum := sha256.Sum256(raw)
	var task AgentTask
	if hex.EncodeToString(sum[:]) != digest || json.Unmarshal(raw, &task) != nil || task.ID != execution.TaskID || task.Kind != execution.Kind || task.Attempt != execution.Attempt || task.Attempt <= 0 {
		return nil, nil
	}
	switch task.Kind {
	case "agent.update", "agent.decommission", "node.ip-quality", "node.network-quality", "node.return-route", "node.international-bandwidth", "node.host-profile", "meridian.link-bandwidth", "meridian.link-bandwidth-server", "node.listener.apply", "landing.server.apply", "landing.proxy.apply", "xray.configuration.inspect", "xray.configuration.apply":
		return &task, nil
	case "application.apply":
		if task.AppKey != meridianAppKey || task.Manifest.ID != "meridian" || catalog.ValidateApp(task.Manifest) != nil {
			return nil, nil
		}
		var matches bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE id=? AND agent_id=? AND app_key=? AND application_id=? AND attempt=?)`, task.ID, execution.AgentID, task.AppKey, task.ApplicationID, task.Attempt).Scan(&matches); err != nil {
			return nil, err
		}
		if matches {
			return &task, nil
		}
	case "application.command":
		if task.MeridianRuntime == nil || task.MeridianRuntime.Validate() != nil {
			return nil, nil
		}
		var matches bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_commands WHERE id=? AND agent_id=? AND gateway_node_id=agent_id AND kind=? AND attempt=?)`, task.ID, execution.AgentID, meridianruntime.ApplyKind, task.Attempt).Scan(&matches); err != nil {
			return nil, err
		}
		if matches {
			return &task, nil
		}
	}
	return nil, nil
}

// Settlement is one database transaction. It abandons reviewed executions and
// their exact business attempts without running commands, asserting success,
// clearing evidence, activating the replacement or releasing its task fence.
func (s *Store) SettleAgentReinstallLocalWork(ctx context.Context, agentID, adminID string, input AgentReinstallLocalWorkInput) (AgentReinstallLocalDisposition, error) {
	var result AgentReinstallLocalDisposition
	if !input.ConfirmLocal || input.OperationID == "" || len(input.PlanRevision) != 64 {
		return result, errors.New("center: explicitly confirm the reviewed old-machine executions")
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
		return result, errors.New("center: old-machine isolation and authorized replacement enrollment are required before settlement")
	}
	var active bool
	var key []byte
	if err = tx.QueryRowContext(ctx, `SELECT status='active' AND credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=?),x25519_public_key FROM agents WHERE id=?`, adminID, agentID).Scan(&active, &key); err != nil {
		return result, err
	}
	digest := sha256.Sum256(key)
	if !active || hex.EncodeToString(digest[:]) != op.ReplacementFingerprint {
		return result, errors.New("center: replacement identity changed; review recovery before continuing")
	}
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT disposition_json FROM agent_reinstall_local_dispositions WHERE operation_id=? AND plan_revision=?`, op.ID, input.PlanRevision).Scan(&saved)
	if err == nil {
		err = json.Unmarshal(saved, &result)
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
		return result, errors.New("center: recovery plan changed; review it again before confirming")
	}
	result = AgentReinstallLocalDisposition{PlanRevision: input.PlanRevision, ExecutionIDs: []string{}, AuthorizedBy: adminID, DisposedAt: s.now().UTC()}
	now := result.DisposedAt.Format(time.RFC3339Nano)
	for _, execution := range plan.Executions {
		if execution.AgentID != agentID || execution.Resolution != "local_after_isolation" {
			continue
		}
		task, err := s.reinstallLocalExecutionTask(ctx, tx, execution)
		if err != nil {
			return result, err
		}
		if task == nil {
			return result, errors.New("center: previous execution evidence changed; inspect it before settlement")
		}
		query, args, err := executionAbandonStatement(*task, agentID, now)
		switch task.Kind {
		case "agent.update":
			query, args, err = `UPDATE agent_updates SET state='failed',last_error='Abandoned during reinstall recovery',lease_expires_at='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('failed','installing','running','pending')`, []any{now, task.ID, agentID, task.Attempt}, nil
		case "agent.decommission":
			query, args, err = `UPDATE agent_decommissions SET state='abandoned',last_error='Abandoned during reinstall recovery',lease_expires_at='',callback_token_hash=X'',updated_at=? WHERE agent_id=? AND attempt=? AND state IN ('failed','cleaning','running','pending')`, []any{now, agentID, task.Attempt}, nil
		}
		if err != nil {
			return result, err
		}
		updated, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return result, err
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			return result, errors.New("center: previous task attempt changed; inspect it before settlement")
		}
		if task.Kind == "application.apply" {
			if _, err := tx.ExecContext(ctx, `UPDATE applications SET status='failed',updated_at=?
			 WHERE id=? AND (SELECT id FROM deployments WHERE application_id=applications.id ORDER BY created_at DESC,rowid DESC LIMIT 1)=?`, now, task.ApplicationID, task.ID); err != nil {
				return result, err
			}
		}
		updated, err = tx.ExecContext(ctx, `UPDATE task_executions SET disposition='abandon',disposition_note='Old-machine local execution isolated by reinstall recovery',disposition_actor=?,disposed_at=?,updated_at=?
		 WHERE id=? AND agent_id=? AND identity_retired_at<>'' AND state IN ('unknown','failed') AND disposition=''`, adminID, now, now, execution.ID, agentID)
		if err != nil {
			return result, err
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			return result, errExecutionAuthorization
		}
		result.ExecutionIDs = append(result.ExecutionIDs, execution.ID)
	}
	if len(result.ExecutionIDs) == 0 {
		return result, errors.New("center: no isolated local executions are eligible for settlement")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_local_dispositions(operation_id,plan_revision,disposition_json) VALUES(?,?,?)`, op.ID, input.PlanRevision, encoded); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Server) handleSettleAgentReinstallLocalWork(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallLocalWorkInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.SettleAgentReinstallLocalWork(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
