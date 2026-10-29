package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
	"github.com/petauron/vastora/internal/tailscalehost"
	"golang.org/x/mod/semver"
)

const tailscaleUpdateSchema = `CREATE TABLE tailscale_update_tasks (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 target_version TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','running','awaiting_decision','succeeded','failed','abandoned')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
)`

type TailscaleUpdateView struct {
	ID            string `json:"id"`
	TargetVersion string `json:"targetVersion"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	UpdatedAt     string `json:"updatedAt"`
}

func (s *Store) queueTailscaleUpdate(ctx context.Context, agentID, adminID string) (TailscaleUpdateView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TailscaleUpdateView{}, err
	}
	defer tx.Rollback()
	var seen, status, revoked, capabilities string
	if err := tx.QueryRowContext(ctx, `SELECT last_seen_at,status,credential_revoked_at,capabilities_json FROM agents WHERE id=?`, agentID).Scan(&seen, &status, &revoked, &capabilities); err != nil {
		return TailscaleUpdateView{}, errors.New("center: node is unavailable")
	}
	last, err := time.Parse(time.RFC3339Nano, seen)
	if err != nil || status != "active" || revoked != "" || !last.After(s.now().Add(-agentConnectedMaxAge)) {
		return TailscaleUpdateView{}, errors.New("center: node must be online")
	}
	var caps NodeCapabilities
	if json.Unmarshal([]byte(capabilities), &caps) != nil || caps.TailscaleUpdate == nil {
		return TailscaleUpdateView{}, errors.New("center: node has not reported a Tailscale check")
	}
	observed := caps.TailscaleUpdate
	checked, err := time.Parse(time.RFC3339Nano, observed.CheckedAt)
	target := tailscalehost.UpdateTask{TargetVersion: observed.LatestVersion}
	if err != nil || checked.After(s.now().Add(5*time.Minute)) || checked.Before(s.now().Add(-48*time.Hour)) || observed.Error != "" || !observed.UpgradeReady || !observed.UpdateAvailable || target.Validate() != nil || observed.InstalledVersion != observed.RunningVersion || !semver.IsValid("v"+observed.InstalledVersion) || semver.Compare("v"+observed.InstalledVersion, "v"+target.TargetVersion) >= 0 {
		return TailscaleUpdateView{}, errors.New("center: fresh supported Tailscale version evidence is required")
	}
	var lastSuccess string
	err = tx.QueryRowContext(ctx, `SELECT updated_at FROM tailscale_update_tasks WHERE agent_id=? AND state='succeeded' ORDER BY updated_at DESC LIMIT 1`, agentID).Scan(&lastSuccess)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TailscaleUpdateView{}, err
	}
	if err == nil {
		completed, parseErr := time.Parse(time.RFC3339Nano, lastSuccess)
		if parseErr != nil || !checked.After(completed) {
			return TailscaleUpdateView{}, errors.New("center: refresh the Tailscale version after the previous update")
		}
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil || paused {
		return TailscaleUpdateView{}, errExecutionBlocked
	}
	if blocked, err := agentReinstallBlocked(ctx, tx, agentID); err != nil || blocked {
		return TailscaleUpdateView{}, errExecutionBlocked
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tailscale_update_tasks WHERE state IN ('pending','running','awaiting_decision')) OR EXISTS(SELECT 1 FROM tailscale_update_tasks WHERE agent_id=? AND state='failed') OR EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, agentID, agentID).Scan(&busy); err != nil {
		return TailscaleUpdateView{}, err
	}
	if busy {
		return TailscaleUpdateView{}, errors.New("center: another update or unresolved execution requires attention")
	}
	token, err := randomToken(18)
	if err != nil {
		return TailscaleUpdateView{}, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	view := TailscaleUpdateView{ID: tailscalehost.UpdateTaskPrefix + token, TargetVersion: target.TargetVersion, State: "pending", UpdatedAt: stamp}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tailscale_update_tasks(id,agent_id,target_version,state,actor_id,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?)`, view.ID, agentID, view.TargetVersion, adminID, stamp, stamp); err != nil {
		return TailscaleUpdateView{}, err
	}
	if err := s.recordTaskEvent(ctx, tx, view.ID, agentID, tailscalehost.UpdateKind, 1, "queued", "Administrator requested a Tailscale update"); err != nil {
		return TailscaleUpdateView{}, err
	}
	if err := tx.Commit(); err != nil {
		return TailscaleUpdateView{}, err
	}
	return view, nil
}

func (s *Store) claimTailscaleUpdate(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, target string
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id,target_version,attempt FROM tailscale_update_tasks WHERE agent_id=? AND state='pending'`, agentID).Scan(&id, &target, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	input := tailscalehost.UpdateTask{TargetVersion: target}
	if input.Validate() != nil {
		return nil, errors.New("center: invalid Tailscale target")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE tailscale_update_tasks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	return &AgentTask{ID: id, Kind: tailscalehost.UpdateKind, Attempt: attempt + 1, Revision: 1, TailscaleUpdate: &input}, nil
}

func (s *Store) completeTailscaleUpdate(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, uncertain bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, lease string
	if err := tx.QueryRowContext(ctx, `SELECT state,lease_expires_at FROM tailscale_update_tasks WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&state, &lease); err != nil {
		return errStaleTaskLease
	}
	expiry, err := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || err != nil || !expiry.After(s.now()) {
		return errStaleTaskLease
	}
	if succeeded {
		return errors.New("center: Tailscale update must be verified after helper handoff")
	}
	next, message := "failed", "Update preflight failed; inspect the node before retrying"
	if uncertain {
		message = "Update dispatch outcome is uncertain; inspect the node before retrying"
	}
	if !uncertain && strings.TrimSpace(taskError) != "" {
		message = controlplane.SafeError(taskError)
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE tailscale_update_tasks SET state=?,error=?,lease_expires_at='',updated_at=? WHERE id=?`, next, message, stamp, id); err != nil {
		return err
	}
	event := "failed"
	if err := s.recordTaskEvent(ctx, tx, id, agentID, tailscalehost.UpdateKind, 1, event, message); err != nil {
		return err
	}
	return commit(tx)
}

// Handoff consumes the exact task authorization before systemd is asked to
// start. A lost response never permits re-execution of the old grant.
func (s *Store) beginTailscaleUpdate(ctx context.Context, agentID, credential, taskID, executionID, sessionID string, attempt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authenticateAgentInQuery(ctx, tx, agentID, credential); err != nil {
		return err
	}
	now := s.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='helper_running',phase='start',updated_at=?,expires_at=? WHERE id=? AND agent_id=? AND task_id=? AND attempt=? AND session_id=? AND kind='tailscale.update' AND state='running' AND phase='handoff' AND disposition='' AND expires_at>? AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`, stamp, now.Add(30*time.Minute).Format(time.RFC3339Nano), executionID, agentID, taskID, attempt, sessionID, stamp, agentID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	result, err = tx.ExecContext(ctx, `UPDATE tailscale_update_tasks SET state='awaiting_decision',lease_expires_at='',error='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state='running' AND lease_expires_at>?`, stamp, taskID, agentID, attempt, stamp)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errStaleTaskLease
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, tailscalehost.UpdateKind, 1, "claimed", "Waiting for the update service and reconnect"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) handleStartTailscaleUpdate(w http.ResponseWriter, r *http.Request) {
	credential, err := agentCredential(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		ExecutionID string `json:"executionId"`
		SessionID   string `json:"sessionId"`
		Attempt     int64  `json:"attempt"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.beginTailscaleUpdate(r.Context(), r.PathValue("id"), credential, r.PathValue("taskID"), input.ExecutionID, input.SessionID, input.Attempt); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"started": true})
}

func (s *Store) activeTailscaleUpdateTask(ctx context.Context, agentID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tailscale_update_tasks WHERE agent_id=? AND state IN ('running','awaiting_decision') ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Store) reconcileTailscaleUpdate(ctx context.Context, tx *sql.Tx, agentID string, observation *tailscalehost.UpdateObservation, now time.Time) error {
	if observation == nil || tailscalehost.UpdateUnitName(observation.TaskID) == "" {
		return nil
	}
	var target string
	err := tx.QueryRowContext(ctx, `SELECT target_version FROM tailscale_update_tasks WHERE id=? AND agent_id=? AND state='awaiting_decision'`, observation.TaskID, agentID).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state, message := "", ""
	if observation.State == "succeeded" && observation.InstalledVersion == target && observation.RunningVersion == target {
		state = "succeeded"
	}
	if observation.State == "failed" {
		state, message = "failed", "Package update failed; inspect the node before retrying"
	}
	if state == "" {
		return nil
	}
	var executionID, sessionID string
	var attempt int64
	err = tx.QueryRowContext(ctx, `SELECT id,session_id,attempt FROM task_executions WHERE agent_id=? AND task_id=? AND kind='tailscale.update' AND state='helper_running' AND phase='start' AND disposition='' AND expires_at>?`, agentID, observation.TaskID, now.UTC().Format(time.RFC3339Nano)).Scan(&executionID, &sessionID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(executionResultEvidence{Result: json.RawMessage(`{}`), Succeeded: state == "succeeded"})
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, evidence, []byte("execution-result:"+executionID))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_executions SET sealed_result=?,phase='result_received',last_error=?,updated_at=? WHERE id=? AND agent_id=? AND task_id=? AND attempt=? AND state='helper_running' AND phase='start' AND disposition=''`, sealed, message, now.UTC().Format(time.RFC3339Nano), executionID, agentID, observation.TaskID, attempt)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	result, err = tx.ExecContext(ctx, `UPDATE tailscale_update_tasks SET state=?,error=?,updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state='awaiting_decision'`, state, message, now.UTC().Format(time.RFC3339Nano), observation.TaskID, agentID, attempt)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errStaleTaskLease
	}
	if err := validateExecutionProjection(ctx, tx, executionID, state == "succeeded"); err != nil {
		return err
	}
	if err := s.finalizeExecution(ctx, tx, agentID, sessionID, executionID); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, observation.TaskID, agentID, tailscalehost.UpdateKind, 1, state, message)
}

func (s *Server) handleQueueTailscaleUpdate(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	view, err := s.store.queueTailscaleUpdate(r.Context(), r.PathValue("id"), adminID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handleAcknowledgeTailscaleUpdate(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		ConfirmStopped bool   `json:"confirmStopped"`
		Note           string `json:"note"`
	}
	if err := decodeJSON(r, &input); err != nil || !input.ConfirmStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 500 {
		writeError(w, http.StatusBadRequest, errors.New("center: confirm the update stopped and record the review"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	stamp := s.store.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(r.Context(), `UPDATE tailscale_update_tasks SET state='abandoned',error='Reviewed by administrator',updated_at=? WHERE id=? AND agent_id=? AND (state='failed' OR (state IN ('awaiting_decision','pending') AND updated_at<?))`, stamp, r.PathValue("taskID"), r.PathValue("id"), s.store.now().Add(-20*time.Minute).UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeError(w, http.StatusConflict, errors.New("center: update is not eligible for review"))
		return
	}
	safeNote := controlplane.SafeError(strings.TrimSpace(input.Note))
	if _, err := tx.ExecContext(r.Context(), `UPDATE task_executions SET state=CASE WHEN state='helper_running' THEN 'unknown' ELSE state END,disposition='abandon',disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE task_id=? AND agent_id=? AND kind='tailscale.update' AND disposition='' AND state IN ('failed','unknown','helper_running')`, safeNote, adminID, stamp, stamp, r.PathValue("taskID"), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.recordTaskEvent(r.Context(), tx, r.PathValue("taskID"), r.PathValue("id"), tailscalehost.UpdateKind, 1, "failed", "Reviewed by administrator: "+safeNote); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"acknowledged": true})
}
