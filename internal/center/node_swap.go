package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/nodeswap"
)

const nodeSwapSchema = `CREATE TABLE node_swap_tasks (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);`

type NodeSwapView struct {
	ID        string         `json:"id"`
	State     string         `json:"state"`
	Error     string         `json:"error,omitempty"`
	Result    *nodeswap.View `json:"result,omitempty"`
	UpdatedAt string         `json:"updatedAt,omitempty"`
}

func (s *Store) NodeSwap(ctx context.Context, agentID string) (NodeSwapView, error) {
	var v NodeSwapView
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT id,state,error,result_json,updated_at FROM node_swap_tasks WHERE agent_id=?`, agentID).Scan(&v.ID, &v.State, &v.Error, &raw, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeSwapView{State: "unconfigured"}, nil
	}
	if err != nil {
		return v, err
	}
	if raw != "null" {
		if json.Unmarshal([]byte(raw), &v.Result) != nil || v.Result == nil || v.Result.Validate() != nil {
			return v, errors.New("center: invalid swap result")
		}
	}
	return v, nil
}

func (s *Store) QueueNodeSwap(ctx context.Context, agentID, adminID string, input nodeswap.Task) (NodeSwapView, error) {
	if err := input.Validate(); err != nil {
		return NodeSwapView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeSwapView{}, err
	}
	defer tx.Rollback()

	var seen string
	var supported bool
	if err = tx.QueryRowContext(ctx, `SELECT last_seen_at,COALESCE(json_extract(capabilities_json,'$.nodeSwap'),0) FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).Scan(&seen, &supported); err != nil {
		return NodeSwapView{}, errors.New("center: node is unavailable")
	}
	last, parseErr := time.Parse(time.RFC3339Nano, seen)
	if parseErr != nil || !last.After(s.now().Add(-agentConnectedMaxAge)) || !supported {
		return NodeSwapView{}, errors.New("center: node must be online with host swap support")
	}
	if paused, pauseErr := executionClaimsPaused(ctx, tx); pauseErr != nil || paused {
		return NodeSwapView{}, errors.New("center: task execution is paused")
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_swap_tasks WHERE agent_id=? AND state IN ('pending','running')) OR EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state IN ('offered','running','helper_running','unknown','failed'))`, agentID, agentID).Scan(&busy); err != nil {
		return NodeSwapView{}, err
	}
	if busy {
		return NodeSwapView{}, errors.New("center: resolve the existing node operation before configuring swap")
	}

	if input.Action == "apply" || input.Action == "restore" {
		var priorState, priorJSON string
		if err = tx.QueryRowContext(ctx, `SELECT state,result_json FROM node_swap_tasks WHERE agent_id=?`, agentID).Scan(&priorState, &priorJSON); err != nil {
			return NodeSwapView{}, errors.New("center: inspect the host swap state before modifying it")
		}
		var prior nodeswap.View
		if priorState != "succeeded" || priorJSON == "null" || json.Unmarshal([]byte(priorJSON), &prior) != nil || prior.Validate() != nil || prior.Revision != input.Revision {
			return NodeSwapView{}, errors.New("center: refresh the host swap inspection before applying or restoring")
		}
		if input.Action == "apply" && !prior.CanApply {
			return NodeSwapView{}, errors.New("center: host is not eligible for managed swap application")
		}
		if input.Action == "restore" && !prior.CanRestore {
			return NodeSwapView{}, errors.New("center: host swap cannot be restored in its inspected state")
		}
	}

	token, err := randomToken(18)
	if err != nil {
		return NodeSwapView{}, err
	}
	id := nodeswap.TaskPrefix + token
	encoded, _ := json.Marshal(input)
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO node_swap_tasks(agent_id,id,input_json,state,actor_id,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?) ON CONFLICT(agent_id) DO UPDATE SET id=excluded.id,input_json=excluded.input_json,state='pending',attempt=0,lease_expires_at='',error='',actor_id=excluded.actor_id,created_at=excluded.created_at,updated_at=excluded.updated_at`, agentID, id, string(encoded), adminID, now, now)
	if err != nil {
		return NodeSwapView{}, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, nodeswap.Kind, 1, "queued", input.Action); err != nil {
		return NodeSwapView{}, err
	}
	if err = tx.Commit(); err != nil {
		return NodeSwapView{}, err
	}
	return s.NodeSwap(ctx, agentID)
}

func (s *Store) claimNodeSwap(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, raw string
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.input_json,q.attempt FROM node_swap_tasks q JOIN agents a ON a.id=q.agent_id WHERE q.agent_id=? AND q.state='pending' AND json_extract(a.capabilities_json,'$.nodeSwap')=1`, agentID).Scan(&id, &raw, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var input nodeswap.Task
	if json.Unmarshal([]byte(raw), &input) != nil || input.Validate() != nil {
		return nil, errors.New("center: invalid swap task")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE node_swap_tasks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND attempt=? AND state='pending'`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	return &AgentTask{ID: id, Kind: nodeswap.Kind, Attempt: attempt + 1, Revision: 1, NodeSwap: &input}, nil
}

func (s *Store) completeNodeSwap(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var state, lease, inputJSON string
	if err = tx.QueryRowContext(ctx, `SELECT state,lease_expires_at,input_json FROM node_swap_tasks WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&state, &lease, &inputJSON); err != nil {
		return errStaleTaskLease
	}
	var input nodeswap.Task
	if json.Unmarshal([]byte(inputJSON), &input) != nil || input.Validate() != nil {
		return errors.New("center: invalid saved swap task")
	}

	target := "failed"
	resultJSON := "null"
	var payload struct {
		NodeSwap *nodeswap.View `json:"nodeSwap"`
	}
	if succeeded {
		if len(raw) > 64*1024 || json.Unmarshal(raw, &payload) != nil || payload.NodeSwap == nil || payload.NodeSwap.ValidateFor(input) != nil {
			return errors.New("center: invalid swap result")
		}
		encoded, _ := json.Marshal(payload.NodeSwap)
		resultJSON = string(encoded)
		target = "succeeded"
		taskError = ""
	} else if len(raw) > 0 && len(raw) <= 64*1024 && json.Unmarshal(raw, &payload) == nil && payload.NodeSwap != nil && payload.NodeSwap.Validate() == nil {
		encoded, _ := json.Marshal(payload.NodeSwap)
		resultJSON = string(encoded)
	}

	if state == target {
		return commit(tx)
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || parseErr != nil || !expires.After(s.now()) {
		return errStaleTaskLease
	}
	if len(taskError) > 1024 {
		taskError = "Host swap operation failed. Inspect the host state."
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_swap_tasks SET state=?,error=?,result_json=CASE WHEN ?='null' THEN result_json ELSE ? END,lease_expires_at='',updated_at=? WHERE id=?`, target, taskError, resultJSON, resultJSON, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, nodeswap.Kind, 1, target, ""); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleNodeSwap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	v, err := s.store.NodeSwap(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleQueueNodeSwap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input nodeswap.Task
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.store.QueueNodeSwap(r.Context(), r.PathValue("id"), adminID, input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
