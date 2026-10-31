package center

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/recovery"
	"github.com/petauron/vastora/internal/secret"
)

// CompleteOSReinstall accepts only a new authenticated Agent session and the
// original identity, operating system and reviewed network. A normal heartbeat
// alone does not complete an armed operation or clear its fence. Once armed,
// expiry forbids further execution, but does not invalidate authenticated proof
// that this exact, undisposed install finished. No new boot grant is issued.
func (s *Store) CompleteOSReinstall(ctx context.Context, agentID, credential, sessionID, executionID string, report osreinstall.ReconnectReport) error {
	bad := func() error { return errors.New("center: reinstall reconnect evidence rejected") }
	if agentID == "" || credential == "" || sessionID == "" || executionID == "" || report.TaskID == "" {
		return bad()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = authenticateAgentInQuery(ctx, tx, agentID, credential); err != nil {
		return bad()
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var taskID, raw, oldSession, taskState, executionState string
	var sealedPlan, publicKey []byte
	err = tx.QueryRowContext(ctx, `SELECT q.id,q.input_json,q.sealed_network,e.session_id,a.x25519_public_key,q.state,e.state
        FROM os_reinstall_tasks q JOIN task_executions e ON e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt
        JOIN agents a ON a.id=q.agent_id JOIN agent_execution_sessions current ON current.agent_id=q.agent_id
        WHERE q.id=? AND q.agent_id=? AND q.state IN ('armed','succeeded') AND q.attempt=1
        AND e.id=? AND e.kind=? AND ((e.state='helper_running' AND e.phase='reinstall_armed' AND e.expires_at<>'') OR (e.state='unknown' AND e.phase='reinstall_armed' AND e.expires_at<>'' AND e.expires_at<=?) OR (e.state='succeeded' AND e.phase='reinstall_verified')) AND e.disposition='' AND e.identity_retired_at='' AND current.session_id=? AND e.session_id<>? AND a.status='active' AND a.credential_revoked_at=''`,
		report.TaskID, agentID, executionID, osreinstall.TaskKind, now, sessionID, sessionID).
		Scan(&taskID, &raw, &sealedPlan, &oldSession, &publicKey, &taskState, &executionState)
	if err != nil || len(publicKey) != 32 {
		return bad()
	}
	var task osreinstall.Task
	if json.Unmarshal([]byte(raw), &task) != nil {
		return bad()
	}
	clear, err := secret.Open(s.key, sealedPlan, []byte("os-reinstall-network:"+taskID))
	if err != nil {
		return bad()
	}
	var network osreinstall.StaticNetworkPlan
	if json.Unmarshal(clear, &network) != nil {
		return bad()
	}
	evidence := osreinstall.ReconnectEvidence{ExpectedAgentID: agentID, ReportedAgentID: agentID,
		ExpectedIdentityHash: task.IdentitySHA256, ReportedIdentityHash: recovery.Digest(publicKey),
		Before: task.Inspection, After: report.After, BeforeNetwork: network, AfterNetwork: report.Network}
	if osreinstall.VerifyReconnect(evidence) != nil {
		return bad()
	}
	// A lost response may be retried without authorizing another install.
	if taskState == "succeeded" && executionState == "succeeded" {
		return tx.Commit()
	}
	if taskState != "armed" || (executionState != "helper_running" && executionState != "unknown") {
		return bad()
	}
	updated, err := tx.ExecContext(ctx, `UPDATE os_reinstall_tasks SET state='succeeded',last_error='',updated_at=?
        WHERE id=? AND agent_id=? AND state='armed' AND attempt=1`, now, taskID, agentID)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return bad()
	}
	updated, err = tx.ExecContext(ctx, `UPDATE task_executions SET state='succeeded',phase='reinstall_verified',last_error='',updated_at=?
        WHERE id=? AND agent_id=? AND task_id=? AND session_id=? AND state IN ('helper_running','unknown') AND phase='reinstall_armed'
        AND disposition='' AND identity_retired_at='' AND expires_at<>'' AND (state='helper_running' OR expires_at<=?)`, now, executionID, agentID, taskID, oldSession, now)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return bad()
	}
	if err = s.recordTaskEvent(ctx, tx, taskID, agentID, osreinstall.TaskKind, 1, "succeeded", "Debian 13 and original network verified"); err != nil {
		return err
	}
	return tx.Commit()
}
