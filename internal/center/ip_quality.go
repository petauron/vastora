package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/ipquality"
)

const ipQualitySchemaSQL = `CREATE TABLE ip_quality_checks (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 address TEXT NOT NULL,
 bind_address TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 checked_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);`

type IPQualityView struct {
	AgentID   string            `json:"agentId"`
	ID        string            `json:"id"`
	State     string            `json:"state"`
	Error     string            `json:"error,omitempty"`
	Report    *ipquality.Report `json:"report,omitempty"`
	CheckedAt string            `json:"checkedAt,omitempty"`
	UpdatedAt string            `json:"updatedAt"`
	Stale     bool              `json:"stale"`
}

// Only the latest report is retained. A new check leaves the previous report
// available, but never relabels its IP or timestamp as the new check's result.
func (s *Store) ListIPQuality(ctx context.Context) ([]IPQualityView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT q.agent_id,q.id,q.state,q.error,q.result_json,q.checked_at,q.updated_at,q.lease_expires_at,a.public_egress_address,
 EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=q.id AND e.agent_id=q.agent_id AND e.attempt=q.attempt AND e.disposition='' AND e.state IN ('unknown','failed'))
 FROM ip_quality_checks q JOIN agents a ON a.id=q.agent_id
 WHERE EXISTS(SELECT 1 FROM services s JOIN applications app ON app.id=s.application_id WHERE app.node_id=q.agent_id AND s.app_protocol='vless/tcp/reality' AND s.status<>'stopped')
    OR EXISTS(SELECT 1 FROM landing_server_states l WHERE l.node_id=q.agent_id AND l.status<>'stopped')
 ORDER BY q.agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []IPQualityView{}
	for rows.Next() {
		var value IPQualityView
		var raw, lease, address string
		var interrupted bool
		if err := rows.Scan(&value.AgentID, &value.ID, &value.State, &value.Error, &raw, &value.CheckedAt, &value.UpdatedAt, &lease, &address, &interrupted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &value.Report); err != nil {
			return nil, err
		}
		if value.Report != nil {
			value.Stale = net.ParseIP(address) == nil || !net.ParseIP(address).Equal(net.ParseIP(value.Report.Address))
		}
		if value.State == "running" {
			expires, err := time.Parse(time.RFC3339Nano, lease)
			if interrupted || err != nil || !expires.After(s.now()) {
				value.State, value.Error = "failed", "interrupted"
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) StartIPQuality(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var capsRaw, lastSeen string
	if err := tx.QueryRowContext(ctx, `SELECT capabilities_json,last_seen_at FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).Scan(&capsRaw, &lastSeen); err != nil {
		return errors.New("ip_quality_node_unavailable")
	}
	seen, err := time.Parse(time.RFC3339Nano, lastSeen)
	if err != nil || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
		return errors.New("ip_quality_node_offline")
	}
	var caps NodeCapabilities
	if json.Unmarshal([]byte(capsRaw), &caps) != nil || !caps.Docker || !caps.IPQuality {
		return errors.New("ip_quality_agent_upgrade_required")
	}
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM services s JOIN applications app ON app.id=s.application_id WHERE app.node_id=? AND s.app_protocol='vless/tcp/reality' AND s.status<>'stopped')
	 OR EXISTS(SELECT 1 FROM landing_server_states l WHERE l.node_id=? AND l.status<>'stopped')`, agentID, agentID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return errors.New("ip_quality_target_required")
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errors.New("ip_quality_tasks_paused")
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') OR EXISTS(SELECT 1 FROM ip_quality_checks WHERE agent_id=? AND state IN ('pending','running'))`, agentID, agentID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errors.New("ip_quality_node_busy")
	}
	egress, err := agentPublicEgress(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if egress == nil {
		return errors.New("ip_quality_address_unavailable")
	}
	input := ipquality.Task{Address: egress.Address, BindAddress: egress.BindAddress}
	if input.Validate() != nil {
		return errors.New("ip_quality_address_unavailable")
	}
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	id = "ip-quality-" + id
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO ip_quality_checks(agent_id,id,address,bind_address,state,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?)
 ON CONFLICT(agent_id) DO UPDATE SET id=excluded.id,address=excluded.address,bind_address=excluded.bind_address,state='pending',attempt=0,lease_expires_at='',error='',created_at=excluded.created_at,updated_at=excluded.updated_at`, agentID, id, input.Address, input.BindAddress, now, now)
	if err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, ipquality.Kind, 1, "queued", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) claimIPQuality(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id string
	var input ipquality.Task
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.address,q.bind_address,q.attempt FROM ip_quality_checks q JOIN agents a ON a.id=q.agent_id WHERE q.agent_id=? AND q.state='pending' AND json_extract(a.capabilities_json,'$.ipQuality')=1`, agentID).Scan(&id, &input.Address, &input.BindAddress, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE ip_quality_checks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND agent_id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	task := &AgentTask{ID: id, Kind: ipquality.Kind, Attempt: attempt + 1, Revision: 1, IPQuality: &input}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, task.Kind, 1, "claimed", ""); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeIPQuality(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var address, state, lease string
	if attempt <= 0 {
		return errStaleTaskLease
	}
	if err := tx.QueryRowContext(ctx, `SELECT address,state,lease_expires_at FROM ip_quality_checks WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&address, &state, &lease); err != nil {
		return errStaleTaskLease
	}
	target, diagnosticError := "succeeded", ""
	var result struct {
		IPQuality *ipquality.Result `json:"ipQuality"`
	}
	if succeeded {
		if len(raw) > ipquality.MaxReportBytes || json.Unmarshal(raw, &result) != nil || result.IPQuality == nil || result.IPQuality.Validate(address) != nil {
			return errors.New("center: invalid IP quality result")
		}
		diagnosticError = result.IPQuality.Error
	} else {
		target, diagnosticError = "failed", "interrupted"
	}
	if state == target {
		return commit(tx)
	}
	expires, err := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || err != nil || !expires.After(s.now()) {
		return errStaleTaskLease
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if succeeded && result.IPQuality.Report != nil {
		encoded, err := json.Marshal(result.IPQuality.Report)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ip_quality_checks SET result_json=?,checked_at=? WHERE id=?`, string(encoded), now, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ip_quality_checks SET state=?,error=?,lease_expires_at='',updated_at=? WHERE id=?`, target, diagnosticError, now, id); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, ipquality.Kind, 1, target, diagnosticError); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleListIPQuality(w http.ResponseWriter, r *http.Request) {
	values, err := s.store.ListIPQuality(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("ip_quality_read_failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": values})
}
func (s *Server) handleStartIPQuality(w http.ResponseWriter, r *http.Request) {
	if err := s.store.StartIPQuality(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}
