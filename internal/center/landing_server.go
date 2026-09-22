package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
 applied_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(applied_json)),
 peer_json BLOB NOT NULL DEFAULT '{}',
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed','stopped')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
)`

// Called inside the topology transaction after resolving managed private
// identities. Never accept a caller-supplied arbitrary host or source address.
func (s *Store) queueLandingServer(ctx context.Context, tx *sql.Tx, nodeID string, plan *landing.ServerPlan) error {
	if frozen, err := meridianCutoverBlocksLandingChanges(ctx, tx); err != nil {
		return err
	} else if frozen {
		return nil
	}
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
	if frozen, err := meridianCutoverBlocksLandingChanges(ctx, tx); err != nil {
		return nil, err
	} else if frozen {
		return nil, nil
	}
	var encoded []byte
	var revision, attempt int64
	readPending := func() error {
		return tx.QueryRowContext(ctx, `SELECT desired_revision,desired_json,attempt FROM landing_server_states
 WHERE node_id=? AND desired_revision>applied_revision AND status = 'pending'`, nodeID).Scan(&revision, &encoded, &attempt)
	}
	err := readPending()
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
	var completed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state='complete')`).Scan(&completed); err != nil {
		return nil, err
	}
	if completed {
		// A pending legacy intent can predate the ownership handoff. Rebuild
		// its source set from current grants before offering it; merely lifting
		// the cutover fence must not authorize an obsolete source again.
		var appliedJSON []byte
		var stillReferenced bool
		if err := tx.QueryRowContext(ctx, `SELECT applied_json,EXISTS(SELECT 1 FROM meridian_route_grants
			WHERE egress_node_id=? AND ((enabled=1 AND status<>'revoked') OR status='revoking'))
			FROM landing_server_states WHERE node_id=?`, nodeID, nodeID).Scan(&appliedJSON, &stillReferenced); err != nil {
			return nil, err
		}
		if stillReferenced {
			var applied landing.ServerState
			if state.Plan == nil || json.Unmarshal(appliedJSON, &applied) != nil || applied.Validate() != nil ||
				applied.NodeID != nodeID || applied.Plan == nil || state.Plan.Address != applied.Plan.Address {
				return nil, errors.New("center: pending landing intent would stop or replace a referenced Meridian service; explicit recovery is required")
			}
		}
		if err := s.refreshClientLandingSources(ctx, tx, nodeID); err != nil {
			return nil, err
		}
		if err := readPending(); err != nil {
			return nil, err
		}
		state = landing.ServerState{}
		if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.NodeID != nodeID || state.Revision != uint64(revision) {
			return nil, errors.New("center: invalid recomposed landing service configuration")
		}
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET status='applying',attempt=attempt+1,lease_expires_at=?,updated_at=?
 WHERE node_id=? AND desired_revision=? AND attempt=? AND status = 'pending'`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nodeID, revision, attempt)
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

func (s *Store) completeLandingServer(ctx context.Context, commit projectionCommit, nodeID string, revision, attempt int64, succeeded bool, peer *landing.PeerIdentity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.projectLandingServer(ctx, tx, commit, nodeID, revision, attempt, succeeded, peer)
}

func (s *Store) projectLandingServer(ctx context.Context, tx *sql.Tx, commit projectionCommit, nodeID string, revision, attempt int64, succeeded bool, peer *landing.PeerIdentity) error {
	var desired, applied, currentAttempt int64
	var status string
	var encoded, previousPeerJSON, appliedJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,attempt,status,desired_json,peer_json,applied_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&desired, &applied, &currentAttempt, &status, &encoded, &previousPeerJSON, &appliedJSON); err != nil {
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
	authorizationChanged := succeeded && !sameLandingServerAuthorization(appliedJSON, state)
	previousAppliedJSON := appliedJSON
	if succeeded {
		applied = desired
		appliedJSON = encoded
		status, message, event = "ready", "", "succeeded"
		if state.Plan == nil {
			status = "stopped"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET applied_revision=?,applied_json=?,peer_json=?,status=?,lease_expires_at='',last_error=?,updated_at=? WHERE node_id=?`, applied, appliedJSON, peerJSON, status, message, s.now().UTC().Format(time.RFC3339Nano), nodeID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, landingServerTaskID(nodeID, revision), nodeID, "landing.server.apply", revision, event, message); err != nil {
		return err
	}
	if !bytes.Equal(previousPeerJSON, peerJSON) {
		if err := s.markMeridianLandingPeerChanged(ctx, tx, nodeID, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	} else if authorizationChanged {
		if err := s.markMeridianLandingAuthorizationChanged(ctx, tx, nodeID, previousAppliedJSON, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if err := s.reconcileGlobalLandingPool(ctx, tx, false); err != nil {
		return err
	}
	return commit(tx)
}

func sameLandingServerAuthorization(previousJSON []byte, current landing.ServerState) bool {
	var previous landing.ServerState
	if json.Unmarshal(previousJSON, &previous) != nil || previous.Validate() != nil || previous.NodeID != current.NodeID {
		return false
	}
	if previous.Plan == nil || current.Plan == nil {
		return previous.Plan == nil && current.Plan == nil
	}
	if previous.Plan.Address != current.Plan.Address {
		return false
	}
	before, beforeErr := landing.MergeSources(previous.Plan.Sources)
	after, afterErr := landing.MergeSources(current.Plan.Sources)
	return beforeErr == nil && afterErr == nil && slices.Equal(before, after)
}
