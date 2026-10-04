package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

const reinstallAcceptanceAuthoritySQL = `EXISTS(SELECT 1 FROM agent_reinstall_client_checks a JOIN agent_reinstall_operations op ON op.id=a.operation_id JOIN agents target ON target.id=op.agent_id WHERE a.command_id=c.id AND c.kind='meridian.recovery.acceptance' AND c.input_json=a.input_json AND c.gateway_node_id=op.agent_id AND c.agent_id<>op.agent_id AND c.application_id=json_extract(a.input_json,'$.applicationId') AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required') AND target.status='active' AND target.credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by))`

type reinstallAcceptanceCommand struct {
	RequestID     string `json:"requestId"`
	Protocol      string `json:"protocol"`
	CredentialID  string `json:"credentialId"`
	EgressID      string `json:"egressId"`
	OperationID   string `json:"operationId"`
	PlanRevision  string `json:"planRevision"`
	ApplicationID string `json:"applicationId"`
	TaskDigest    string `json:"taskDigest"`
}

type AgentReinstallClientCheck struct {
	CommandID string `json:"commandId"`
	State     string `json:"state"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// Durable metadata contains only identity/digest references, never client keys.
func (s *Store) validateReinstallAcceptance(ctx context.Context, tx *sql.Tx, verifier, id string) (meridianruntime.AcceptanceTask, error) {
	var empty meridianruntime.AcceptanceTask
	var raw []byte
	var target, admin string
	err := tx.QueryRowContext(ctx, `SELECT c.input_json,op.agent_id,op.authorized_by FROM application_commands c JOIN agent_reinstall_client_checks a ON a.command_id=c.id JOIN agent_reinstall_operations op ON op.id=a.operation_id WHERE c.id=? AND c.agent_id=? AND c.attempt<=1 AND (c.state<>'pending' OR c.attempt=0) AND c.reconciliation_requested=0 AND c.reconciliation_required=0 AND `+reinstallAcceptanceAuthoritySQL, id, verifier).Scan(&raw, &target, &admin)
	var command reinstallAcceptanceCommand
	if err != nil || json.Unmarshal(raw, &command) != nil {
		return empty, errExecutionAuthorization
	}
	tasks, err := s.reinstallAcceptanceTasks(ctx, tx, target, verifier, admin, AgentReinstallApplicationInput{OperationID: command.OperationID, PlanRevision: command.PlanRevision, ApplicationID: command.ApplicationID})
	if err != nil {
		return empty, err
	}
	for _, task := range tasks {
		digest, err := task.Digest()
		if err == nil && digest == command.TaskDigest {
			return task, nil
		}
	}
	return empty, errExecutionAuthorization
}

func (s *Store) QueueAgentReinstallClientChecks(ctx context.Context, target, verifier, admin, requestID string, input AgentReinstallApplicationInput) ([]AgentReinstallClientCheck, error) {
	if len(requestID) < 8 || len(requestID) > 128 {
		return nil, errors.New("center: client verification request identity is required")
	}
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	receipts, err := s.queueReinstallClientChecks(ctx, tx, target, verifier, admin, requestID, input)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.taskChanges.notify("agent:" + verifier)
	return receipts, nil
}

// Keep the verifier's single-active-command invariant. A successful receipt
// advances the same request to its next original credential/protocol.
func (s *Store) queueReinstallClientChecks(ctx context.Context, tx *sql.Tx, target, verifier, admin, requestID string, input AgentReinstallApplicationInput) ([]AgentReinstallClientCheck, error) {
	tasks, err := s.reinstallAcceptanceTasks(ctx, tx, target, verifier, admin, input)
	if err != nil {
		return nil, err
	}
	var conflict bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id WHERE a.operation_id=? AND c.application_id=? AND (
 (json_extract(a.input_json,'$.requestId')=? AND (json_extract(a.input_json,'$.planRevision')<>? OR c.agent_id<>?)) OR
 (json_extract(a.input_json,'$.requestId')<>? AND (c.state IN ('pending','running') OR c.reconciliation_required=1 OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=c.id AND e.disposition='' AND (e.state='unknown' OR e.phase='result_received'))))))`, input.OperationID, input.ApplicationID, requestID, input.PlanRevision, verifier, requestID).Scan(&conflict)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, errors.New("center: inspect the outstanding client check before starting another request")
	}
	receipts := make([]AgentReinstallClientCheck, 0, len(tasks))
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, task := range tasks {
		digest, err := task.Digest()
		if err != nil {
			return nil, err
		}
		command := reinstallAcceptanceCommand{RequestID: requestID, Protocol: string(task.Client.Protocol), CredentialID: task.Client.Material.Credential.ID, EgressID: task.Client.Material.Credential.EgressID, OperationID: input.OperationID, PlanRevision: input.PlanRevision, ApplicationID: input.ApplicationID, TaskDigest: digest}
		raw, _ := json.Marshal(command)
		var previous AgentReinstallClientCheck
		// Repeated clicks/lost responses return the same command, including a failure.
		// A new request ID explicitly starts another check after terminal evidence.
		err = tx.QueryRowContext(ctx, `SELECT c.id,c.state FROM application_commands c JOIN agent_reinstall_client_checks a ON a.command_id=c.id WHERE a.operation_id=? AND c.agent_id=? AND a.input_json=? ORDER BY c.rowid DESC LIMIT 1`, input.OperationID, verifier, raw).Scan(&previous.CommandID, &previous.State)
		if err == nil {
			receipts = append(receipts, previous)
			if previous.State != "succeeded" {
				break
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		id, err := randomToken(18)
		if err != nil {
			return nil, err
		}
		id = "reinstall-client-" + id
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, input.ApplicationID, verifier, target, meridianruntime.AcceptanceKind, raw, now, now); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_client_checks(command_id,operation_id,input_json) VALUES(?,?,?)`, id, input.OperationID, raw); err != nil {
			return nil, err
		}
		if _, err = s.validateReinstallAcceptance(ctx, tx, verifier, id); err != nil {
			return nil, err
		}
		if err = s.recordTaskEvent(ctx, tx, id, verifier, "application.command", 1, "queued", "Verify the original recovery client through its configured exit"); err != nil {
			return nil, err
		}
		receipts = append(receipts, AgentReinstallClientCheck{CommandID: id, State: "pending"})
		break
	}
	return receipts, nil
}

func (s *Store) completeReinstallAcceptance(ctx context.Context, commit projectionCommit, tx *sql.Tx, id, verifier string, succeeded bool, raw json.RawMessage) error {
	task, err := s.validateReinstallAcceptance(ctx, tx, verifier, id)
	if err != nil {
		return err
	}
	receipt := AgentReinstallClientCheck{CommandID: id, State: "failed"}
	if succeeded {
		var envelope struct {
			Acceptance *meridianruntime.AcceptanceResult `json:"meridianAcceptance"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Acceptance == nil || envelope.Acceptance.Validate(task) != nil {
			return errors.New("center: invalid recovery client result")
		}
		receipt.State = "succeeded"
		receipt.CheckedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	encoded, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_client_checks SET result_json=? WHERE command_id=?`, encoded, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json='{}',error='',lease_expires_at='',updated_at=? WHERE id=?`, receipt.State, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, verifier, "application.command", 1, receipt.State, "Recovery client request finished; remaining recovery requirements still apply"); err != nil {
		return err
	}
	if succeeded {
		var rawInput []byte
		var admin string
		if err = tx.QueryRowContext(ctx, `SELECT c.input_json,op.authorized_by FROM application_commands c JOIN agent_reinstall_client_checks a ON a.command_id=c.id JOIN agent_reinstall_operations op ON op.id=a.operation_id WHERE c.id=?`, id).Scan(&rawInput, &admin); err != nil {
			return err
		}
		var command reinstallAcceptanceCommand
		if err = json.Unmarshal(rawInput, &command); err != nil {
			return err
		}
		if _, err = s.queueReinstallClientChecks(ctx, tx, task.TargetAgentID, verifier, admin, command.RequestID, AgentReinstallApplicationInput{OperationID: command.OperationID, PlanRevision: command.PlanRevision, ApplicationID: command.ApplicationID}); err != nil {
			return err
		}
	}
	return commit(tx)
}

// A successful command row alone is not proof. Require its authenticated
// execution evidence, current reconstructed task and a bounded Center-clock age.
func (s *Store) readReinstallClientCheck(ctx context.Context, tx *sql.Tx, verifier, id string) (AgentReinstallClientCheck, error) {
	receipt := AgentReinstallClientCheck{CommandID: id, State: "needs_review"}
	task, err := s.validateReinstallAcceptance(ctx, tx, verifier, id)
	if err != nil {
		return receipt, nil
	}
	var raw []byte
	var commandState string
	if err = tx.QueryRowContext(ctx, `SELECT a.result_json,c.state FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id WHERE c.id=? AND c.agent_id=?`, id, verifier).Scan(&raw, &commandState); err != nil {
		return receipt, err
	}
	if commandState != "succeeded" {
		receipt.State = commandState
		return receipt, nil
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.CommandID != id || receipt.State != "succeeded" {
		return AgentReinstallClientCheck{CommandID: id, State: "needs_review"}, nil
	}
	receipt.State = "needs_review"
	var executionID, createdAt string
	var sealed []byte
	if err = tx.QueryRowContext(ctx, `SELECT id,created_at,sealed_result FROM task_executions WHERE task_id=? AND agent_id=? AND attempt=1 AND kind='application.command' AND state='succeeded' AND phase='reported' AND identity_retired_at=''`, id, verifier).Scan(&executionID, &createdAt, &sealed); err != nil {
		return receipt, nil
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return receipt, nil
	}
	checked, err := time.Parse(time.RFC3339Nano, receipt.CheckedAt)
	if err != nil || checked.Before(created) || checked.After(s.now()) {
		return receipt, nil
	}
	if s.now().Sub(created) > 10*time.Minute {
		receipt.State = "stale"
		return receipt, nil
	}
	proof, err := secret.Open(s.key, sealed, []byte("execution-result:"+executionID))
	if err != nil {
		return receipt, nil
	}
	var evidence executionResultEvidence
	var result struct {
		Acceptance *meridianruntime.AcceptanceResult `json:"meridianAcceptance"`
	}
	if json.Unmarshal(proof, &evidence) != nil || !evidence.Succeeded || evidence.Unknown || json.Unmarshal(evidence.Result, &result) != nil || result.Acceptance == nil || result.Acceptance.Validate(task) != nil {
		return receipt, nil
	}
	receipt.State = "verified"
	return receipt, nil
}
