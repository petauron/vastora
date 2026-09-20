package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

var errExecutionBlocked = errors.New("center: execution requires operator verification")
var errExecutionAuthorization = errors.New("center: execution authorization is no longer valid")

const executionSchemaSQL = `CREATE TABLE agent_execution_sessions (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE agent_execution_session_history (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id)
);
CREATE TABLE task_executions (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 attempt INTEGER NOT NULL,
 session_id TEXT NOT NULL,
 digest TEXT NOT NULL,
 sealed_task BLOB NOT NULL,
 sealed_result BLOB NOT NULL DEFAULT X'',
 state TEXT NOT NULL CHECK(state IN ('offered','running','helper_running','succeeded','failed','unknown')),
 phase TEXT NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 disposition TEXT NOT NULL DEFAULT '',
 disposition_note TEXT NOT NULL DEFAULT '',
 disposition_actor TEXT NOT NULL DEFAULT '',
 disposed_at TEXT NOT NULL DEFAULT '',
 UNIQUE(task_id, attempt)
);
CREATE INDEX task_executions_agent_unresolved ON task_executions(agent_id, state)
 WHERE disposition='' AND state<>'succeeded';
CREATE TABLE execution_claim_control_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 paused INTEGER NOT NULL CHECK(paused IN (0,1)),
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE execution_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 execution_id TEXT NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
 phase TEXT NOT NULL,
 state TEXT NOT NULL,
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TRIGGER execution_insert_audit AFTER INSERT ON task_executions BEGIN
 INSERT INTO execution_events(execution_id,phase,state,actor,created_at)
 VALUES(NEW.id,NEW.phase,NEW.state,NEW.agent_id,NEW.updated_at);
END;
CREATE TRIGGER execution_update_audit AFTER UPDATE ON task_executions
 WHEN OLD.state<>NEW.state OR OLD.phase<>NEW.phase OR OLD.disposition<>NEW.disposition BEGIN
 INSERT INTO execution_events(execution_id,phase,state,actor,created_at)
 VALUES(NEW.id,NEW.phase,NEW.state,CASE WHEN NEW.disposition_actor<>'' THEN NEW.disposition_actor ELSE NEW.agent_id END,NEW.updated_at);
END;`

type ExecutionView struct {
	ID          string `json:"id"`
	AgentID     string `json:"agentId"`
	TaskID      string `json:"taskId"`
	Kind        string `json:"kind"`
	Attempt     int64  `json:"attempt"`
	State       string `json:"state"`
	Phase       string `json:"phase"`
	LastError   string `json:"lastError"`
	UpdatedAt   string `json:"updatedAt"`
	Disposition string `json:"disposition"`
	CanConfirm  bool   `json:"canConfirm"`
}

// RegisterExecutionSession is an explicit process boundary. A replacement
// process cannot inherit the old process's permission or infer its outcome.
func (s *Store) RegisterExecutionSession(ctx context.Context, agentID, credential, sessionID string, protocol int) error {
	if protocol != controlplane.ExecutionProtocol || len(sessionID) < 24 || len(sessionID) > 128 || strings.ContainsAny(sessionID, " \r\n\t") {
		return errExecutionAuthorization
	}
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	var current string
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM agent_execution_sessions WHERE agent_id=?`, agentID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current == sessionID {
		if err := tx.Commit(); err != nil {
			return err
		}
		s.recoverReceivedExecutionResults(ctx, agentID)
		return nil
	}
	// A retired process cannot re-register its old session after reconnecting.
	// Keep this history at Center, never in the Agent's polling hot path.
	var seen bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_execution_session_history WHERE agent_id=? AND session_id=?)`, agentID, sessionID).Scan(&seen); err != nil {
		return err
	}
	if seen {
		return errExecutionAuthorization
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_execution_session_history(agent_id,session_id,created_at) VALUES(?,?,?)`, agentID, sessionID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='unknown',last_error='Agent process changed; verify the previous execution',updated_at=?
		WHERE agent_id=? AND session_id<>? AND disposition='' AND state IN ('offered','running')`, now, agentID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_execution_sessions(agent_id,session_id,updated_at) VALUES(?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET session_id=excluded.session_id,updated_at=excluded.updated_at`, agentID, sessionID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.recoverReceivedExecutionResults(ctx, agentID)
	return nil
}

func (s *Store) recoverReceivedExecutionResults(ctx context.Context, agentID string) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM task_executions
		WHERE agent_id=? AND state='unknown' AND phase='result_received' AND disposition=''
		ORDER BY created_at,id`, agentID)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return
	}
	if err := rows.Close(); err != nil {
		return
	}
	for _, id := range ids {
		if err := s.projectRetainedExecutionResult(ctx, id, retainedExecutionResolution{automatic: true}); err != nil {
			// Recovery is opportunistic. Any invalid evidence, newer business
			// state, or storage failure leaves the original fence intact for
			// operator inspection, but must not reject the replacement process.
			continue
		}
	}
}

func (s *Store) executionClaimAllowed(ctx context.Context, agentID, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM agent_execution_sessions WHERE agent_id=?`, agentID).Scan(&current); err != nil || current != sessionID {
		return errExecutionAuthorization
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='unknown',last_error='Execution authorization expired; manual verification required',updated_at=?
		WHERE agent_id=? AND disposition='' AND state IN ('offered','running','helper_running') AND expires_at<=?`, now, agentID, now); err != nil {
		return err
	}
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, agentID).Scan(&blocked); err != nil {
		return err
	}
	var explicitRecovery bool
	var independentAgentUpdate bool
	var ordinaryWorkBlocked bool
	if blocked {
		var currentVersion string
		var pending bool
		if err := tx.QueryRowContext(ctx, `SELECT version,EXISTS(SELECT 1 FROM agent_updates WHERE agent_id=agents.id AND state='pending') FROM agents WHERE id=?`, agentID).Scan(&currentVersion, &pending); err != nil {
			return err
		}
		ordinaryWorkBlocked, err = unresolvedExecutionBlocksAgentWork(ctx, tx, agentID, currentVersion)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM xray_configuration_recoveries WHERE agent_id=? AND state='pending')`, agentID).Scan(&explicitRecovery); err != nil {
			return err
		}
		if ordinaryWorkBlocked && !explicitRecovery {
			executionBlocked, err := unresolvedExecutionBlocksAgentUpdate(ctx, tx, agentID, currentVersion)
			if err != nil {
				return err
			}
			independentAgentUpdate = pending && !executionBlocked
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if ordinaryWorkBlocked && !explicitRecovery && !independentAgentUpdate {
		return errExecutionBlocked
	}
	return nil
}

func (s *Store) persistExecutionAuthorization(ctx context.Context, tx *sql.Tx, agentID, sessionID string, task AgentTask) (controlplane.ExecutionAuthorization, error) {
	if task.ID == "" || task.Attempt <= 0 {
		return controlplane.ExecutionAuthorization{}, errExecutionAuthorization
	}
	id, err := randomToken(24)
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	digest := sha256.Sum256(payload)
	sealed, err := secret.Seal(s.key, payload, []byte("execution-task:"+id))
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	now := s.now().UTC()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return controlplane.ExecutionAuthorization{}, err
	} else if paused {
		return controlplane.ExecutionAuthorization{}, errExecutionBlocked
	}
	if task.Kind == "agent.update" {
		var currentVersion string
		if err := tx.QueryRowContext(ctx, `SELECT version FROM agents WHERE id=?`, agentID).Scan(&currentVersion); err != nil {
			return controlplane.ExecutionAuthorization{}, err
		}
		blocked, err := unresolvedExecutionBlocksAgentUpdate(ctx, tx, agentID, currentVersion)
		if err != nil {
			return controlplane.ExecutionAuthorization{}, err
		}
		if blocked {
			return controlplane.ExecutionAuthorization{}, errExecutionBlocked
		}
	} else if task.Kind != "xray.configuration.inspect" && task.Kind != "xray.configuration.apply" {
		var currentVersion string
		if err := tx.QueryRowContext(ctx, `SELECT version FROM agents WHERE id=?`, agentID).Scan(&currentVersion); err != nil {
			return controlplane.ExecutionAuthorization{}, err
		}
		blocked, err := unresolvedExecutionBlocksAgentWork(ctx, tx, agentID, currentVersion)
		if err != nil {
			return controlplane.ExecutionAuthorization{}, err
		}
		if blocked {
			return controlplane.ExecutionAuthorization{}, errExecutionBlocked
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at)
		SELECT ?,?,?,?,?,?,?,?,'offered','authorized',?,?,? WHERE EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		id, agentID, task.ID, task.Kind, task.Attempt, sessionID, hex.EncodeToString(digest[:]), sealed,
		now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, sessionID)
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return controlplane.ExecutionAuthorization{}, errExecutionBlocked
	}
	return controlplane.ExecutionAuthorization{ID: id, Protocol: controlplane.ExecutionProtocol, Digest: hex.EncodeToString(digest[:])}, nil
}

// StartExecution consumes an offer once. A lost response deliberately leaves an
// unresolved execution; repeating this call cannot repeat the side effect.
func (s *Store) StartExecution(ctx context.Context, agentID, sessionID, id, digest string) error {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE task_executions SET state='running',phase='started',updated_at=?,expires_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND digest=? AND state='offered' AND disposition='' AND expires_at>?
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		now.Format(time.RFC3339Nano), now.Add(taskLeaseDuration).Format(time.RFC3339Nano), id, agentID, sessionID, digest, now.Format(time.RFC3339Nano), agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	// Token-based cloudflared reads ingress from Cloudflare, not the task JSON.
	// Do this only after consuming the one-use offer, outside a DB transaction.
	if err := s.prepareTunnelExecution(ctx, agentID, sessionID, id); err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		message := "center: Cloudflare Tunnel origin configuration was not confirmed; explicit recovery required"
		return errors.Join(errors.New(message), s.StopExecution(stopCtx, agentID, sessionID, id, true, message))
	}
	return nil
}

// CheckExecutionStep records only a public phase identifier, never a command,
// argument list, path or credential. The caller must stop on any returned error.
func (s *Store) CheckExecutionStep(ctx context.Context, agentID, sessionID, id, phase string) error {
	switch phase {
	case "prepare", "apply", "persist", "report", "handoff":
	default:
		return errors.New("center: invalid execution phase")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE task_executions SET phase=?,updated_at=?,expires_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND state='running' AND phase<>'result_received' AND disposition='' AND expires_at>?
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`, phase,
		now.Format(time.RFC3339Nano), now.Add(taskLeaseDuration).Format(time.RFC3339Nano), id, agentID, sessionID, now.Format(time.RFC3339Nano), agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

// RenewExecution extends permission without rewriting the last confirmed phase.
// It cannot resurrect an expired grant or authorize post-result mutations.
func (s *Store) RenewExecution(ctx context.Context, agentID, sessionID, id string) error {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE task_executions SET expires_at=?,updated_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND state='running' AND phase<>'result_received' AND disposition='' AND expires_at>?
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID, sessionID, now.Format(time.RFC3339Nano), agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

// StopExecution preserves the last confirmed phase. Cancellation does not prove
// that an external operation was undone, so uncertain outcomes remain fenced.
// A late stop must not replace already persisted result evidence.
func (s *Store) StopExecution(ctx context.Context, agentID, sessionID, id string, unknown bool, taskError string) error {
	state := "failed"
	if unknown {
		state = "unknown"
	}
	message := controlplane.SafeError(taskError)
	if len(message) > 1024 {
		message = message[:1024]
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE task_executions SET state=?,last_error=?,updated_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND state IN ('offered','running') AND phase<>'result_received' AND disposition=''
		AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`,
		state, message, now, id, agentID, sessionID, agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

// executionResultEvidence preserves the original projection inputs. In
// particular, a missing runtime generation must not be reconstructed from a
// newer heartbeat when an operator later resolves an uncertain result.
type executionResultEvidence struct {
	Result                       json.RawMessage `json:"result"`
	Succeeded                    bool            `json:"succeeded"`
	Unknown                      bool            `json:"unknown"`
	ApplicationRuntimeGeneration *int            `json:"applicationRuntimeGeneration"`
}

func (s *Store) StoreExecutionResult(ctx context.Context, agentID, sessionID, id string, result json.RawMessage, succeeded, unknown bool, taskError string, runtimeGeneration *int) error {
	if !json.Valid(result) || len(result) > 2<<20 {
		return errors.New("center: invalid execution result")
	}
	encoded, err := json.Marshal(executionResultEvidence{Result: result, Succeeded: succeeded, Unknown: unknown, ApplicationRuntimeGeneration: runtimeGeneration})
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, []byte("execution-result:"+id))
	if err != nil {
		return err
	}
	// Keep the fence until the business projection has committed as well. A
	// crash between evidence persistence and projection is an unknown outcome.
	message := controlplane.SafeError(taskError)
	if len(message) > 1024 {
		message = message[:1024]
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	updated, err := s.db.ExecContext(ctx, `UPDATE task_executions SET sealed_result=?,phase='result_received',last_error=?,updated_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND state IN ('running','helper_running') AND phase<>'result_received' AND disposition='' AND expires_at>?
		AND (state<>'helper_running' OR ?=0 OR phase='start')
		AND (state='helper_running' OR EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?))`, sealed, message, now, id, agentID, sessionID, now, succeeded, agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

// projectionCommit finalizes the execution in the business projection's own
// transaction. Evidence may be retained separately, but a completed projection
// must never become visible without its matching execution outcome.
type projectionCommit func(*sql.Tx) error

func (s *Store) finalizeExecution(ctx context.Context, tx *sql.Tx, agentID, sessionID, id string) error {
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed_result FROM task_executions WHERE id=? AND agent_id=? AND session_id=? AND state IN ('running','helper_running') AND phase='result_received' AND disposition=''`, id, agentID, sessionID).Scan(&sealed); err != nil {
		return errExecutionAuthorization
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-result:"+id))
	if err != nil {
		return errors.New("center: execution result evidence is invalid")
	}
	var evidence struct {
		Succeeded bool `json:"succeeded"`
		Unknown   bool `json:"unknown"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return errors.New("center: execution result evidence is invalid")
	}
	state := "failed"
	if evidence.Succeeded {
		state = "succeeded"
	}
	if evidence.Unknown {
		state = "unknown"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE task_executions SET state=?,phase='reported',updated_at=?
		WHERE id=? AND agent_id=? AND session_id=? AND state IN ('running','helper_running') AND phase='result_received' AND disposition='' AND expires_at>?
		AND (state='helper_running' OR EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?))`, state,
		now, id, agentID, sessionID, now, agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

type ExecutionPage struct {
	Executions []ExecutionView `json:"executions"`
	NextCursor int64           `json:"nextCursor"`
}

func (s *Store) ListExecutions(ctx context.Context, before int64) (ExecutionPage, error) {
	if before < 0 {
		return ExecutionPage{}, errors.New("center: invalid execution cursor")
	}
	query := `SELECT rowid,id,agent_id,task_id,kind,attempt,state,phase,last_error,updated_at,disposition,sealed_result FROM task_executions`
	var args []any
	if before > 0 {
		query += ` WHERE rowid<?`
		args = append(args, before)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY rowid DESC LIMIT 101`, args...)
	if err != nil {
		return ExecutionPage{}, err
	}
	defer rows.Close()
	page := ExecutionPage{Executions: []ExecutionView{}}
	var last int64
	for rows.Next() {
		if len(page.Executions) == 100 {
			page.NextCursor = last
			break
		}
		var value ExecutionView
		var sealedResult []byte
		if err := rows.Scan(&last, &value.ID, &value.AgentID, &value.TaskID, &value.Kind, &value.Attempt, &value.State, &value.Phase, &value.LastError, &value.UpdatedAt, &value.Disposition, &sealedResult); err != nil {
			return ExecutionPage{}, fmt.Errorf("center: read execution: %w", err)
		}
		if value.Disposition == "" && (value.State == "failed" || value.State == "unknown") && len(sealedResult) != 0 {
			if raw, err := secret.Open(s.key, sealedResult, []byte("execution-result:"+value.ID)); err == nil {
				var evidence executionResultEvidence
				value.CanConfirm = json.Unmarshal(raw, &evidence) == nil && retainedResultSupportsConfirmation(value.Kind, evidence)
			}
		}
		page.Executions = append(page.Executions, value)
	}
	return page, rows.Err()
}
