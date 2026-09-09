package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

const landingProxySchema = `CREATE TABLE landing_proxy_states (
 node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 landing_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 server_revision INTEGER NOT NULL,
 source_address TEXT NOT NULL,
 health_revision INTEGER NOT NULL DEFAULT 0,
 health_ok INTEGER NOT NULL DEFAULT 0 CHECK(health_ok IN (0,1)),
 health_checked_at TEXT NOT NULL DEFAULT '',
 health_received_at TEXT NOT NULL DEFAULT '',
 desired_revision INTEGER NOT NULL CHECK(desired_revision > 0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 desired_json BLOB NOT NULL CHECK(json_valid(desired_json)),
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed','stopped')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
)`

func landingProxyTaskID(nodeID string, revision int64) string {
	return fmt.Sprintf("landing-proxy-%s-r%d", nodeID, revision)
}
func landingProxyTaskRevision(id string) (int64, bool) {
	i := strings.LastIndex(id, "-r")
	if !strings.HasPrefix(id, "landing-proxy-") || i <= len("landing-proxy-") {
		return 0, false
	}
	r, err := strconv.ParseInt(id[i+2:], 10, 64)
	return r, err == nil && r > 0
}

func (s *Store) claimLandingProxyTask(ctx context.Context, tx *sql.Tx, nodeID string) (*AgentTask, error) {
	var revision, attempt int64
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT p.desired_revision,p.attempt,p.desired_json FROM landing_proxy_states p
 JOIN landing_server_states server ON server.node_id=p.landing_node_id
 WHERE p.node_id=? AND p.desired_revision>p.applied_revision AND p.status='pending'
 AND (json_extract(p.desired_json,'$.proxy') IS NULL OR (server.applied_revision>=p.server_revision AND server.status='ready'))`, nodeID).Scan(&revision, &attempt, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state landing.DesiredState
	if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.Server != nil || state.NodeID != nodeID || state.Revision != uint64(revision) {
		return nil, errors.New("center: invalid landing proxy task")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET status='applying',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE node_id=? AND desired_revision=? AND attempt=? AND status='pending'`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nodeID, revision, attempt)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, errStaleTaskLease
	}
	task := &AgentTask{Kind: "landing.proxy.apply", ID: landingProxyTaskID(nodeID, revision), Attempt: attempt + 1, Revision: revision, LandingProxyState: &state}
	if err := s.recordTaskEvent(ctx, tx, task.ID, nodeID, task.Kind, revision, "claimed", "landing proxy task claimed"); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeLandingProxy(ctx context.Context, nodeID string, revision, attempt int64, succeeded bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desired, applied, currentAttempt int64
	var status string
	var encoded []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,attempt,status,desired_json FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&desired, &applied, &currentAttempt, &status, &encoded); err != nil {
		return err
	}
	if revision < desired || revision <= applied || (revision == desired && attempt < currentAttempt) {
		return nil
	}
	if revision != desired || attempt != currentAttempt {
		return errStaleTaskLease
	}
	if status != "applying" {
		return nil
	}
	var state landing.DesiredState
	if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.NodeID != nodeID || state.Revision != uint64(revision) {
		return errors.New("center: invalid landing proxy completion")
	}
	message, event := "Landing configuration failed; retry the operation.", "failed"
	status = "failed"
	if succeeded {
		applied = desired
		status, message, event = "ready", "", "succeeded"
		if state.Proxy == nil {
			status = "stopped"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET applied_revision=?,status=?,lease_expires_at='',last_error=?,updated_at=? WHERE node_id=?`, applied, status, message, s.now().UTC().Format(time.RFC3339Nano), nodeID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, landingProxyTaskID(nodeID, revision), nodeID, "landing.proxy.apply", revision, event, message); err != nil {
		return err
	}
	if succeeded {
		if err := s.retireLandingSources(ctx, tx, nodeID, state.Proxy != nil); err != nil {
			return err
		}
	}
	return tx.Commit()
}
