package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/recovery"
	"github.com/petauron/vastora/internal/secret"
)

type OSReinstallRequest struct {
	InspectionID string `json:"inspectionId"`
	ConfirmDisk  string `json:"confirmDisk"`
	Username     string `json:"username"`
	SSHPort      int    `json:"sshPort"`
	ConfirmErase bool   `json:"confirmErase"`
}

type OSReinstallView struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	CanRecheck bool   `json:"canRecheck"`
	UpdatedAt  string `json:"updatedAt"`
}

// QueueOSReinstall only records an explicit operator request against a fresh
// inspection. It neither starts an installer nor treats an IP as a complete
// network plan. Agents without the complete execution capability are rejected.
func (s *Store) QueueOSReinstall(ctx context.Context, agentID, adminID string, input OSReinstallRequest) (OSReinstallView, error) {
	bad := func(message string) (OSReinstallView, error) {
		return OSReinstallView{}, errors.New("center: " + message)
	}
	agentID, adminID = strings.TrimSpace(agentID), strings.TrimSpace(adminID)
	if agentID == "" || adminID == "" || input.InspectionID == "" || input.ConfirmDisk == "" || !input.ConfirmErase {
		return bad("confirm the reviewed system disk and erase action")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OSReinstallView{}, err
	}
	defer tx.Rollback()
	var seen, ownership, runtimeRecovery string
	var version string
	var capabilitiesJSON, publicKey, rolesJSON []byte
	var installed int
	err = tx.QueryRowContext(ctx, `SELECT last_seen_at,tailscale_ownership,runtime_recovery,version,capabilities_json,x25519_public_key,roles_json,applied_installations
		FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).
		Scan(&seen, &ownership, &runtimeRecovery, &version, &capabilitiesJSON, &publicKey, &rolesJSON, &installed)
	if err != nil {
		return bad("node is unavailable")
	}
	last, err := time.Parse(time.RFC3339Nano, seen)
	var capabilities NodeCapabilities
	var roles []string
	if err != nil || !last.After(s.now().Add(-agentConnectedMaxAge)) ||
		json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.OSReinstall ||
		json.Unmarshal(rolesJSON, &roles) != nil || len(roles) != 1 || roles[0] != "worker" ||
		ownership != "" || runtimeRecovery != "" || installed != 0 || len(publicKey) != 32 || version != Version {
		return bad("online, compatible unused node and supported reconnect path are required")
	}
	var applications, gateway, busy bool
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM applications WHERE node_id=?),
		EXISTS(SELECT 1 FROM site_gateways WHERE agent_id=?),
		EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')
		OR EXISTS(SELECT 1 FROM os_reinstall_tasks WHERE agent_id=? AND state NOT IN ('succeeded','cancelled'))`,
		agentID, agentID, agentID, agentID).Scan(&applications, &gateway, &busy); err != nil {
		return OSReinstallView{}, err
	}
	if applications || gateway || busy {
		return bad("node contains managed workloads or an unresolved operation")
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil || paused {
		return bad("task execution is paused")
	}

	var state, raw, checked string
	err = tx.QueryRowContext(ctx, `SELECT state,result_json,checked_at FROM node_diagnostic_checks
		WHERE agent_id=? AND id=? AND kind=? AND pair_key=? AND targets_json=?`,
		agentID, input.InspectionID, nodediagnostics.HostProfileKind, osReinstallInspectionPairKey, osReinstallInspectionTargets).
		Scan(&state, &raw, &checked)
	if err != nil || state != "succeeded" {
		return bad("a fresh matching inspection is required")
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil || checkedAt.After(s.now().Add(time.Minute)) || !checkedAt.After(s.now().Add(-5*time.Minute)) {
		return bad("inspection expired; inspect the node again")
	}
	var result nodediagnostics.Result
	if json.Unmarshal([]byte(raw), &result) != nil || result.Validate(nodediagnostics.HostProfileKind) != nil ||
		result.Host == nil || result.Host.OSReinstall == nil || result.Host.OSReinstall.Disk.Path != input.ConfirmDisk {
		return bad("inspection no longer matches the selected disk")
	}
	password, err := randomToken(32)
	if err != nil {
		return OSReinstallView{}, err
	}
	task := osreinstall.Task{Inspection: *result.Host.OSReinstall, Username: strings.TrimSpace(input.Username),
		SSHPort: input.SSHPort, IdentitySHA256: recovery.Digest(publicKey), RecoveryPassword: password}
	if task.Validate() != nil {
		return bad("host or access is outside the supported reinstall scope")
	}
	token, err := randomToken(18)
	if err != nil {
		return OSReinstallView{}, err
	}
	id := "os-reinstall-" + token
	sealed, err := secret.Seal(s.key, []byte(password), []byte("os-reinstall-password:"+id))
	if err != nil {
		return OSReinstallView{}, err
	}
	task.RecoveryPassword = ""
	encoded, err := json.Marshal(task)
	if err != nil {
		return OSReinstallView{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO os_reinstall_tasks
		(id,agent_id,actor_id,inspection_id,input_json,sealed_password,state,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'pending',?,?)`, id, agentID, adminID, input.InspectionID, string(encoded), sealed, now, now)
	if err != nil {
		return OSReinstallView{}, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, osreinstall.TaskKind, 1, "queued", "Debian 13 system disk reinstall requested"); err != nil {
		return OSReinstallView{}, err
	}
	if err = tx.Commit(); err != nil {
		return OSReinstallView{}, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return OSReinstallView{ID: id, AgentID: agentID, State: "pending", UpdatedAt: now}, nil
}

func (s *Store) OSReinstallTask(ctx context.Context, agentID string) (*OSReinstallView, error) {
	var view OSReinstallView
	err := s.db.QueryRowContext(ctx, `SELECT q.id,q.agent_id,
        CASE WHEN q.state='armed' AND EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt AND e.state='unknown') THEN 'unknown' ELSE q.state END,
        CASE WHEN q.state='armed' AND EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt AND e.state='unknown') THEN 'Reinstall outcome requires review' ELSE q.last_error END,q.updated_at,
        (q.state='failed' OR (q.state='unknown' AND COALESCE(length(q.encrypted_backup),0)=0 AND COALESCE(length(q.sealed_network),0)=0)) AND EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt AND e.state IN ('failed','unknown') AND e.disposition='')
        AND NOT EXISTS(SELECT 1 FROM task_executions e JOIN execution_events ev ON ev.execution_id=e.id WHERE e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt AND (ev.phase='reinstall_armed' OR ev.state='helper_running'))
        FROM os_reinstall_tasks q WHERE q.agent_id=? ORDER BY q.created_at DESC,q.rowid DESC LIMIT 1`, agentID).
		Scan(&view.ID, &view.AgentID, &view.State, &view.Error, &view.UpdatedAt, &view.CanRecheck)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *Store) claimOSReinstall(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, bool, error) {
	var id, raw, seen, ownership, runtimeRecovery, version, status, revoked, checked string
	var sealed, publicKey, rolesJSON, capabilitiesJSON []byte
	var attempt int64
	var installed int
	var applications, gateway bool
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.input_json,q.sealed_password,q.attempt,a.last_seen_at,a.tailscale_ownership,
        a.runtime_recovery,a.version,a.status,a.credential_revoked_at,a.x25519_public_key,a.roles_json,a.capabilities_json,a.applied_installations,
        COALESCE((SELECT checked_at FROM node_diagnostic_checks WHERE id=q.inspection_id AND agent_id=q.agent_id),''),
        EXISTS(SELECT 1 FROM applications WHERE node_id=q.agent_id),EXISTS(SELECT 1 FROM site_gateways WHERE agent_id=q.agent_id)
        FROM os_reinstall_tasks q JOIN agents a ON a.id=q.agent_id WHERE q.agent_id=? AND q.state='pending' AND q.attempt=0`, agentID).
		Scan(&id, &raw, &sealed, &attempt, &seen, &ownership, &runtimeRecovery, &version, &status, &revoked, &publicKey, &rolesJSON, &capabilitiesJSON, &installed, &checked, &applications, &gateway)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var payload osreinstall.Task
	var roles []string
	var capabilities NodeCapabilities
	password, openErr := secret.Open(s.key, sealed, []byte("os-reinstall-password:"+id))
	parseErr := json.Unmarshal([]byte(raw), &payload)
	payload.RecoveryPassword = string(password)
	last, lastErr := time.Parse(time.RFC3339Nano, seen)
	checkedAt, checkedErr := time.Parse(time.RFC3339Nano, checked)
	now := s.now().UTC()
	unsafe := openErr != nil || parseErr != nil || payload.Validate() != nil || json.Unmarshal(rolesJSON, &roles) != nil ||
		json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.OSReinstall || len(roles) != 1 || roles[0] != "worker" ||
		lastErr != nil || !last.After(now.Add(-agentConnectedMaxAge)) || checkedErr != nil || !checkedAt.After(now.Add(-5*time.Minute)) || checkedAt.After(now.Add(time.Minute)) ||
		ownership != "" || runtimeRecovery != "" || version != Version || status != "active" || revoked != "" || installed != 0 || applications || gateway ||
		len(publicKey) != 32 || recovery.Digest(publicKey) != payload.IdentitySHA256
	if unsafe {
		stamp := now.Format(time.RFC3339Nano)
		changed, e := tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state='cancelled',last_error='Node changed since inspection; inspect again',updated_at=? WHERE id=? AND agent_id=? AND state='pending' AND attempt=0`, stamp, id, agentID)
		if e != nil {
			return nil, false, e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return nil, false, errStaleTaskLease
		}
		if e = s.recordTaskEvent(ctx, tx, id, agentID, osreinstall.TaskKind, 1, "failed", "Node changed before reinstall was claimed"); e != nil {
			return nil, false, e
		}
		return nil, true, nil
	}
	changed, err := tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state='running',attempt=1,lease_expires_at=?,updated_at=?
        WHERE id=? AND agent_id=? AND state='pending' AND attempt=0`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID)
	if err != nil {
		return nil, false, err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return nil, false, errStaleTaskLease
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, osreinstall.TaskKind, 1, "claimed", ""); err != nil {
		return nil, false, err
	}
	return &AgentTask{ID: id, Kind: osreinstall.TaskKind, Attempt: 1, Revision: 1, OSReinstall: &payload}, false, nil
}

// The normal task-completion endpoint may report only failure. A success is
// reserved for authenticated post-boot identity, OS and network evidence.
func (s *Store) completeOSReinstallFailure(ctx context.Context, commit projectionCommit, agentID, taskID string, attempt int64, succeeded bool, taskError string, uncertain bool) error {
	if succeeded || attempt != 1 {
		return errors.New("center: reinstall success requires post-boot verification")
	}
	message := controlplane.SafeError(taskError)
	if message == "" {
		message = "Reinstall execution did not complete"
	}
	if len(message) > 1024 {
		message = message[:1024]
	}
	state := "failed"
	if uncertain {
		state = "unknown"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	changed, err := tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state=?,lease_expires_at='',last_error=?,updated_at=?
		WHERE id=? AND agent_id=? AND attempt=? AND state IN ('running','backup_ready') AND lease_expires_at>?`,
		state, message, now, taskID, agentID, attempt, now)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return errStaleTaskLease
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, osreinstall.TaskKind, 1, "failed", "Reinstall requires operator review"); err != nil {
		return err
	}
	return commit(tx)
}

// RetireFailedOSReinstall releases a reported failure, or an uncertain attempt
// with no saved backup/network, only before any boot grant. History is retained.
func (s *Store) RetireFailedOSReinstall(ctx context.Context, agentID, adminID string) error {
	var executionID string
	err := s.db.QueryRowContext(ctx, `SELECT e.id FROM os_reinstall_tasks q JOIN task_executions e ON e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt
        WHERE q.agent_id=? AND (q.state='failed' OR (q.state='unknown' AND COALESCE(length(q.encrypted_backup),0)=0 AND COALESCE(length(q.sealed_network),0)=0)) AND e.state IN ('failed','unknown') AND e.disposition='' ORDER BY q.created_at DESC LIMIT 1`, agentID).Scan(&executionID)
	if err != nil {
		return errors.New("center: no safely stopped reinstall failure is available")
	}
	return s.AbandonExecution(ctx, executionID, adminID, controlplane.ExecutionDisposition{Action: "abandon", ExecutionStopped: true, Note: "Stopped before reinstall boot authorization; fresh inspection required."})
}
