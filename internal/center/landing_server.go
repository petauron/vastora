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

const landingServerSchema = `CREATE TABLE landing_server_states (
 node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 desired_revision INTEGER NOT NULL CHECK(desired_revision > 0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 desired_json BLOB NOT NULL CHECK(json_valid(desired_json)),
 peer_json BLOB NOT NULL DEFAULT '{}',
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed','stopped')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
)`

// Called inside the topology transaction after resolving managed private
// identities. Never accept a caller-supplied arbitrary host or source address.
func (s *Store) queueLandingServer(ctx context.Context, tx *sql.Tx, nodeID string, plan *landing.ServerPlan) error {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM landing_server_states WHERE node_id = ?`, nodeID).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	revision++
	state := landing.ServerState{NodeID: nodeID, Revision: uint64(revision)}
	if plan != nil {
		copy := *plan
		copy.Revision = state.Revision
		state.Plan = &copy
	}
	if err := state.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,desired_json,status,updated_at)
 VALUES(?,?,?,'pending',?) ON CONFLICT(node_id) DO UPDATE SET desired_revision=excluded.desired_revision,
 desired_json=excluded.desired_json,status='pending',lease_expires_at='',last_error='',updated_at=excluded.updated_at`,
		nodeID, revision, encoded, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('landing_policy_revision','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, landingServerTaskID(nodeID, revision), nodeID, "landing.server.apply", revision, "queued", "landing service configuration queued")
}

func landingServerTaskID(nodeID string, revision int64) string {
	return fmt.Sprintf("landing-server-%s-r%d", nodeID, revision)
}

func landingServerTaskRevision(taskID string) (int64, bool) {
	index := strings.LastIndex(taskID, "-r")
	if !strings.HasPrefix(taskID, "landing-server-") || index <= len("landing-server-") {
		return 0, false
	}
	revision, err := strconv.ParseInt(taskID[index+2:], 10, 64)
	return revision, err == nil && revision > 0
}

func (s *Store) claimLandingServerTask(ctx context.Context, tx *sql.Tx, nodeID string) (*AgentTask, error) {
	var encoded []byte
	var revision, attempt int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision,desired_json,attempt FROM landing_server_states
 WHERE node_id=? AND desired_revision>applied_revision AND status IN ('pending','failed')`, nodeID).Scan(&revision, &encoded, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state landing.ServerState
	if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.NodeID != nodeID || state.Revision != uint64(revision) {
		return nil, errors.New("center: invalid landing service configuration")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET status='applying',attempt=attempt+1,lease_expires_at=?,updated_at=?
 WHERE node_id=? AND desired_revision=? AND attempt=? AND status IN ('pending','failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nodeID, revision, attempt)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, errStaleTaskLease
	}
	task := &AgentTask{Kind: "landing.server.apply", ID: landingServerTaskID(nodeID, revision), Attempt: attempt + 1, Revision: revision, LandingServerState: &state}
	if err := s.recordTaskEvent(ctx, tx, task.ID, nodeID, task.Kind, revision, "claimed", "landing service task claimed"); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeLandingServer(ctx context.Context, nodeID string, revision, attempt int64, succeeded bool, peer *landing.PeerIdentity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desired, applied, currentAttempt int64
	var status string
	var encoded []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,attempt,status,desired_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&desired, &applied, &currentAttempt, &status, &encoded); err != nil {
		return err
	}
	if revision < desired || revision <= applied || revision == desired && attempt < currentAttempt {
		return nil
	}
	if revision != desired || attempt != currentAttempt {
		return errStaleTaskLease
	}
	if status != "applying" {
		return nil
	}
	state := landing.ServerState{}
	if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.NodeID != nodeID || state.Revision != uint64(revision) {
		return errors.New("center: invalid landing completion state")
	}
	status, message, event := "failed", "Landing service configuration failed; retry the operation.", "failed"
	peerJSON := []byte("{}")
	if succeeded && state.Plan != nil {
		if peer == nil || peer.ID == "" || peer.PublicKey == "" || peer.Address != state.Plan.Address {
			return errors.New("center: landing node did not confirm its private identity")
		}
		peerJSON, _ = json.Marshal(peer)
	}
	if succeeded {
		applied = desired
		status, message, event = "ready", "", "succeeded"
		if state.Plan == nil {
			status = "stopped"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET applied_revision=?,status=?,lease_expires_at='',last_error=?,updated_at=? WHERE node_id=?`, applied, status, message, s.now().UTC().Format(time.RFC3339Nano), nodeID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, landingServerTaskID(nodeID, revision), nodeID, "landing.server.apply", revision, event, message); err != nil {
		return err
	}
	if succeeded {
		if _, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET peer_json=? WHERE node_id=?`, peerJSON, nodeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
