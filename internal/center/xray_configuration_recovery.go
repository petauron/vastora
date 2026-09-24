package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/petauron/vastora/internal/xrayrecovery"
)

const xrayConfigurationRecoverySchemaSQL = `CREATE TABLE xray_configuration_recoveries (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 action TEXT NOT NULL CHECK(action IN ('inspect','runtime','agent_state')),
 state TEXT NOT NULL CHECK(state IN ('pending','running','awaiting_decision','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 expected_runtime_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_revision INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);`

type XrayConfigurationRecoveryView struct {
	AgentID       string               `json:"agentId"`
	ApplicationID string               `json:"applicationId"`
	ID            string               `json:"id"`
	Action        string               `json:"action"`
	State         string               `json:"state"`
	Error         string               `json:"error,omitempty"`
	Result        *xrayrecovery.Result `json:"result,omitempty"`
	UpdatedAt     string               `json:"updatedAt"`
}

func (s *Store) XrayConfigurationRecovery(ctx context.Context, agentID string) (XrayConfigurationRecoveryView, error) {
	var view XrayConfigurationRecoveryView
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,application_id,id,action,state,error,result_json,updated_at FROM xray_configuration_recoveries WHERE agent_id=?`, agentID).Scan(&view.AgentID, &view.ApplicationID, &view.ID, &view.Action, &view.State, &view.Error, &raw, &view.UpdatedAt)
	if err != nil {
		return view, err
	}
	if raw != "null" {
		var result xrayrecovery.Result
		if json.Unmarshal([]byte(raw), &result) != nil {
			return view, errors.New("center: invalid stored Xray configuration recovery result")
		}
		view.Result = &result
	}
	return view, nil
}

func (s *Store) StartXrayConfigurationInspection(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	applicationID, err := xrayWorkerApplicationID(ctx, tx, agentID)
	if err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM xray_configuration_recoveries WHERE agent_id=? AND state IN ('pending','running'))`, agentID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errors.New("center: Xray configuration recovery is already running")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	id := "xray-recovery-" + uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO xray_configuration_recoveries(agent_id,application_id,id,action,state,created_at,updated_at) VALUES(?,?,?,'inspect','pending',?,?)
	 ON CONFLICT(agent_id) DO UPDATE SET application_id=excluded.application_id,id=excluded.id,action='inspect',state='pending',attempt=0,lease_expires_at='',expected_runtime_sha256='',expected_agent_sha256='',expected_agent_revision=0,error='',result_json='null',created_at=excluded.created_at,updated_at=excluded.updated_at`, agentID, applicationID, id, now, now)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.taskChanges.notify("agent:" + agentID)
	return nil
}

func (s *Store) StartXrayConfigurationApply(ctx context.Context, agentID, source string) error {
	if source != xrayrecovery.RuntimeSource && source != xrayrecovery.AgentSource {
		return errors.New("center: invalid Xray configuration authority")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, state, raw string
	if err := tx.QueryRowContext(ctx, `SELECT application_id,state,result_json FROM xray_configuration_recoveries WHERE agent_id=?`, agentID).Scan(&applicationID, &state, &raw); err != nil {
		return err
	}
	if state != "awaiting_decision" {
		return errors.New("center: inspect Xray configuration before selecting an authority")
	}
	if current, err := xrayWorkerApplicationID(ctx, tx, agentID); err != nil || current != applicationID {
		return errors.New("center: Xray worker application changed; inspect again")
	}
	var result xrayrecovery.Result
	if json.Unmarshal([]byte(raw), &result) != nil || result.Validate(xrayrecovery.InspectKind) != nil {
		return errors.New("center: stored Xray inspection is invalid")
	}
	if source == xrayrecovery.RuntimeSource && !result.RuntimeImportable {
		return errors.New("center: current Xray configuration cannot become Agent state")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	id := "xray-recovery-" + uuid.NewString()
	updated, err := tx.ExecContext(ctx, `UPDATE xray_configuration_recoveries SET id=?,action=?,state='pending',attempt=0,lease_expires_at='',expected_runtime_sha256=?,expected_agent_sha256=?,expected_agent_revision=?,error='',updated_at=? WHERE agent_id=? AND state='awaiting_decision'`, id, source, result.RuntimeSHA256, result.AgentSHA256, result.AgentRevision, now, agentID)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errStaleTaskLease
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.taskChanges.notify("agent:" + agentID)
	return nil
}

func xrayWorkerApplicationID(ctx context.Context, tx *sql.Tx, agentID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT application.id FROM applications application WHERE application.node_id=? AND application.status IN ('running','failed') AND (
		(application.app_key='vastora-official/3x-ui' AND application.role='worker') OR
		(application.app_key='vastora-official/meridian' AND EXISTS(
			SELECT 1 FROM meridian_endpoints endpoint JOIN meridian_cutover cutover ON cutover.id=1
			WHERE endpoint.application_id=application.id AND endpoint.legacy_retired=0
			AND cutover.subscription_authority='meridian' AND cutover.state IN ('project','verify','retire')
		))
	) ORDER BY application.created_at DESC LIMIT 1`, agentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: managed Xray worker was not found")
	}
	return id, err
}

func (s *Store) claimXrayConfigurationRecovery(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, applicationID, action, runtimeHash, agentHash string
	var revision uint64
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id,application_id,action,expected_runtime_sha256,expected_agent_sha256,expected_agent_revision,attempt FROM xray_configuration_recoveries WHERE agent_id=? AND state='pending'`, agentID).Scan(&id, &applicationID, &action, &runtimeHash, &agentHash, &revision, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	kind := xrayrecovery.ApplyKind
	if action == "inspect" {
		kind = xrayrecovery.InspectKind
	}
	input := xrayrecovery.Task{Action: action, ApplicationID: applicationID, ExpectedRuntimeSHA256: runtimeHash, ExpectedAgentSHA256: agentHash, ExpectedAgentRevision: revision}
	if err := input.Validate(kind); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE xray_configuration_recoveries SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND agent_id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID, attempt)
	if err != nil {
		return nil, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return nil, errStaleTaskLease
	}
	task := &AgentTask{ID: id, Kind: kind, Attempt: attempt + 1, Revision: 1, XrayRecovery: &input}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, kind, 1, "claimed", ""); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeXrayConfigurationRecovery(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var action, state, lease string
	if attempt <= 0 || tx.QueryRowContext(ctx, `SELECT action,state,lease_expires_at FROM xray_configuration_recoveries WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&action, &state, &lease) != nil {
		return errStaleTaskLease
	}
	expires, err := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || err != nil || !expires.After(s.now()) {
		return errStaleTaskLease
	}
	target := "failed"
	message := strings.TrimSpace(taskError)
	resultJSON := "null"
	if succeeded {
		kind := xrayrecovery.ApplyKind
		if action == "inspect" {
			kind = xrayrecovery.InspectKind
		}
		var envelope struct {
			XrayRecovery *xrayrecovery.Result `json:"xrayRecovery"`
		}
		if len(raw) > xrayrecovery.MaxResultBytes || json.Unmarshal(raw, &envelope) != nil || envelope.XrayRecovery == nil || envelope.XrayRecovery.Validate(kind) != nil || envelope.XrayRecovery.Action != action {
			return errors.New("center: invalid Xray configuration recovery result")
		}
		encoded, err := json.Marshal(envelope.XrayRecovery)
		if err != nil {
			return err
		}
		resultJSON = string(encoded)
		if action == "inspect" {
			target = "awaiting_decision"
		} else {
			target = "succeeded"
		}
		message = ""
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE xray_configuration_recoveries SET state=?,error=?,result_json=CASE WHEN ?='null' THEN result_json ELSE ? END,lease_expires_at='',updated_at=? WHERE id=?`, target, message, resultJSON, resultJSON, now, id); err != nil {
		return err
	}
	if succeeded && action != "inspect" {
		var applicationID string
		if err := tx.QueryRowContext(ctx, `SELECT application_id FROM xray_configuration_recoveries WHERE id=?`, id).Scan(&applicationID); err != nil {
			return err
		}
		// A recovery attempt may have no task_executions row. An empty
		// exclusion set must still release earlier fenced executions.
		if _, err := tx.ExecContext(ctx, `UPDATE task_executions SET disposition='configuration-recovered',disposition_note='Explicit Xray configuration recovery completed',disposition_actor='system',disposed_at=?,updated_at=?
		 WHERE agent_id=? AND id NOT IN (SELECT id FROM task_executions WHERE task_id=? AND attempt=? ORDER BY created_at DESC LIMIT 1) AND disposition='' AND state<>'succeeded'
		 AND ((kind='application.apply' AND task_id IN (SELECT id FROM deployments WHERE application_id=?)) OR (kind='application.command' AND task_id IN (SELECT id FROM application_commands WHERE application_id=?)) OR kind IN ('xray.configuration.inspect','xray.configuration.apply'))`, now, now, agentID, id, attempt, applicationID, applicationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET reconciliation_required=0,reconciliation_requested=0,updated_at=?
		 WHERE application_id=? AND agent_id=? AND state='failed' AND reconciliation_required=1
		 AND id IN (SELECT task_id FROM task_executions WHERE agent_id=? AND disposition='configuration-recovered')`, now, applicationID, agentID, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status='running',updated_at=? WHERE id=?`, now, applicationID); err != nil {
			return err
		}
	}
	event := "failed"
	if succeeded {
		event = "succeeded"
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, map[bool]string{true: xrayrecovery.InspectKind, false: xrayrecovery.ApplyKind}[action == "inspect"], 1, event, message); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleGetXrayConfigurationRecovery(writer http.ResponseWriter, request *http.Request) {
	view, err := s.store.XrayConfigurationRecovery(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"recovery": nil})
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("xray_recovery_read_failed"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"recovery": view})
}

func (s *Server) handleInspectXrayConfiguration(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.StartXrayConfigurationInspection(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) handleApplyXrayConfigurationRecovery(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Source string `json:"source"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.StartXrayConfigurationApply(request.Context(), request.PathValue("id"), input.Source); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"queued": true})
}
