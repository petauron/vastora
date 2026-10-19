package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/nodesecurity"
)

const nodeSecuritySchema = `CREATE TABLE node_security_tasks (
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

type NodeSecurityView struct {
	ID        string             `json:"id"`
	State     string             `json:"state"`
	Error     string             `json:"error,omitempty"`
	Result    *nodesecurity.View `json:"result,omitempty"`
	UpdatedAt string             `json:"updatedAt,omitempty"`
}

func (s *Store) NodeSecurity(ctx context.Context, agentID string) (NodeSecurityView, error) {
	var v NodeSecurityView
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT id,state,error,result_json,updated_at FROM node_security_tasks WHERE agent_id=?`, agentID).Scan(&v.ID, &v.State, &v.Error, &raw, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeSecurityView{State: "unconfigured"}, nil
	}
	if err != nil {
		return v, err
	}
	if raw != "null" {
		if json.Unmarshal([]byte(raw), &v.Result) != nil || v.Result == nil || v.Result.Validate() != nil {
			return v, errors.New("center: invalid security result")
		}
		// An old pending receipt cannot establish that a local watchdog has not
		// rolled back while Center was offline. Request a new inspection.
		if v.Result.State == "pending_confirmation" {
			deadline, e := time.Parse(time.RFC3339Nano, v.Result.Deadline)
			if e != nil || !s.now().Before(deadline) {
				v.Result.State = "action_required"
				v.Result.Reasons = []string{"Confirmation deadline expired. Refresh actual host state."}
			}
		}
	}
	return v, nil
}

func (s *Store) QueueNodeSecurity(ctx context.Context, agentID, adminID string, input nodesecurity.Task) (NodeSecurityView, error) {
	if err := input.Validate(); err != nil {
		return NodeSecurityView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeSecurityView{}, err
	}
	defer tx.Rollback()
	var seen string
	var supported bool
	if err = tx.QueryRowContext(ctx, `SELECT last_seen_at,COALESCE(json_extract(capabilities_json,'$.nodeSecurity'),0) FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).Scan(&seen, &supported); err != nil {
		return NodeSecurityView{}, errors.New("center: node is unavailable")
	}
	last, e := time.Parse(time.RFC3339Nano, seen)
	if e != nil || !last.After(s.now().Add(-agentConnectedMaxAge)) || !supported {
		return NodeSecurityView{}, errors.New("center: node must be online with security-task support")
	}
	if paused, e := executionClaimsPaused(ctx, tx); e != nil || paused {
		return NodeSecurityView{}, errors.New("center: task execution is paused")
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_security_tasks WHERE agent_id=? AND state IN ('pending','running')) OR EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state IN ('offered','running','helper_running','unknown','failed'))`, agentID, agentID).Scan(&busy); err != nil {
		return NodeSecurityView{}, err
	}
	if busy {
		return NodeSecurityView{}, errors.New("center: resolve the existing node operation before configuring security")
	}
	if input.Action == "preview" || input.Action == "apply" {
		if err = preserveSecurityPublicPorts(ctx, tx, agentID, input.Policy); err != nil {
			return NodeSecurityView{}, err
		}
	}
	if input.Action == "confirm" {
		// Require fresh independent control/business evidence after application;
		// host confirmation additionally requires a fresh SSH-session receipt.
		var resultJSON string
		if err = tx.QueryRowContext(ctx, `SELECT result_json FROM node_security_tasks WHERE agent_id=?`, agentID).Scan(&resultJSON); err != nil {
			return NodeSecurityView{}, err
		}
		var prior nodesecurity.View
		if json.Unmarshal([]byte(resultJSON), &prior) != nil || prior.Revision != input.Revision || prior.State != "pending_confirmation" {
			return NodeSecurityView{}, errors.New("center: refresh the pending security revision first")
		}
		deadline, e := time.Parse(time.RFC3339Nano, prior.Deadline)
		if e != nil || !s.now().Before(deadline) {
			return NodeSecurityView{}, errors.New("center: security confirmation deadline expired")
		}
		if _, e := time.Parse(time.RFC3339Nano, prior.AppliedAt); e != nil {
			return NodeSecurityView{}, errors.New("center: missing actual firewall application evidence")
		}
		appliedAt := prior.AppliedAt
		var bad bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states WHERE node_id=? AND status<>'stopped' AND (status<>'ready' OR desired_revision<>applied_revision OR health_received_at<?)) OR EXISTS(SELECT 1 FROM meridian_route_grants r JOIN meridian_endpoints e ON e.id=r.endpoint_id JOIN applications a ON a.id=e.application_id WHERE (a.node_id=? OR r.egress_node_id=? OR r.ipv4_egress_node_id=?) AND r.enabled=1 AND (r.runtime_healthy=0 OR r.health_expires_unix_ms<=?))`, agentID, s.now().Add(-30*time.Second).UTC().Format(time.RFC3339Nano), agentID, agentID, agentID, s.now().UnixMilli()).Scan(&bad); err != nil {
			return NodeSecurityView{}, err
		}
		if bad {
			return NodeSecurityView{}, errors.New("center: private/landing business health must be fresh and healthy before confirmation")
		}
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states WHERE node_id=? AND status<>'stopped' AND (health_received_at<? OR COALESCE(json_extract(health_json,'$.healthy'),0)<>1)) OR EXISTS(SELECT 1 FROM meridian_route_grants r JOIN meridian_endpoints e ON e.id=r.endpoint_id JOIN applications a ON a.id=e.application_id WHERE (a.node_id=? OR r.egress_node_id=? OR r.ipv4_egress_node_id=?) AND r.enabled=1 AND (COALESCE(json_extract(r.health_diagnostics_json,'$.status'),'')<>'ready' OR EXISTS(SELECT 1 FROM json_each(r.health_diagnostics_json,'$.legs') WHERE COALESCE(json_extract(value,'$.startedAt'),'')<?)))`, agentID, appliedAt, agentID, agentID, agentID, appliedAt).Scan(&bad); err != nil {
			return NodeSecurityView{}, err
		}
		if bad {
			return NodeSecurityView{}, errors.New("center: wait for new private/proxy health evidence after firewall application")
		}
	}
	if input.Action == "apply" {
		var priorJSON, state string
		if err = tx.QueryRowContext(ctx, `SELECT state,result_json FROM node_security_tasks WHERE agent_id=?`, agentID).Scan(&state, &priorJSON); err != nil {
			return NodeSecurityView{}, errors.New("center: preview the security policy before applying")
		}
		var prior nodesecurity.View
		if state != "succeeded" || json.Unmarshal([]byte(priorJSON), &prior) != nil || prior.State != "preview" || !prior.Supported || prior.Revision != input.Revision || prior.Policy.Digest() != input.Policy.Digest() {
			return NodeSecurityView{}, errors.New("center: a successful matching security preview is required")
		}
	}
	token, err := randomToken(18)
	if err != nil {
		return NodeSecurityView{}, err
	}
	id := "node-security-" + token
	if input.Action == "preview" {
		sum := sha256.Sum256([]byte(id + ":" + input.Policy.Digest()))
		input.Revision = hex.EncodeToString(sum[:])
	}
	encoded, _ := json.Marshal(input)
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO node_security_tasks(agent_id,id,input_json,state,actor_id,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?) ON CONFLICT(agent_id) DO UPDATE SET id=excluded.id,input_json=excluded.input_json,state='pending',attempt=0,lease_expires_at='',error='',actor_id=excluded.actor_id,created_at=excluded.created_at,updated_at=excluded.updated_at`, agentID, id, string(encoded), adminID, now, now)
	if err != nil {
		return NodeSecurityView{}, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, nodesecurity.Kind, 1, "queued", input.Action); err != nil {
		return NodeSecurityView{}, err
	}
	if err = tx.Commit(); err != nil {
		return NodeSecurityView{}, err
	}
	return s.NodeSecurity(ctx, agentID)
}

func preserveSecurityPublicPorts(ctx context.Context, tx *sql.Tx, agentID string, p nodesecurity.Policy) error {
	// Ordinary proxy UDP replies use connection tracking. FullCone/P2P and
	// different-source replies are outside this baseline's explicit MVP scope.
	required := []nodesecurity.Rule{}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM node_listener_states WHERE node_id=? AND status<>'stopped'`, agentID).Scan(&raw); err == nil {
		var state gateway.NodeListenerState
		if json.Unmarshal(raw, &state) != nil {
			return errors.New("center: invalid node listener state")
		}
		if len(state.Listener.Routes) > 0 {
			required = append(required, nodesecurity.Rule{Protocol: "tcp", Port: state.Listener.Port})
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM gateway_states WHERE gateway_node_id=? AND status<>'stopped'`, agentID).Scan(&raw); err == nil {
		var state gateway.DesiredState
		if json.Unmarshal(raw, &state) != nil {
			return errors.New("center: invalid gateway state")
		}
		for _, listener := range state.Listeners {
			if listener.Kind == "public" {
				for _, port := range []int{listener.HTTPPort, listener.HTTPSPort} {
					if port > 0 {
						required = append(required, nodesecurity.Rule{Protocol: "tcp", Port: port})
					}
				}
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var hy2 bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND e.status<>'retired' AND e.hy2_enabled=1)`, agentID).Scan(&hy2); err != nil {
		return err
	}
	if hy2 {
		required = append(required, nodesecurity.Rule{Protocol: "udp", Port: 443})
	}
	for _, rule := range required {
		if !slices.Contains(p.PublicRules, rule) {
			return errors.New("center: approved rules omit an existing managed public service port")
		}
	}
	return nil
}

func (s *Store) claimNodeSecurity(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, raw string
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.input_json,q.attempt FROM node_security_tasks q JOIN agents a ON a.id=q.agent_id WHERE q.agent_id=? AND q.state='pending' AND json_extract(a.capabilities_json,'$.nodeSecurity')=1`, agentID).Scan(&id, &raw, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var input nodesecurity.Task
	if json.Unmarshal([]byte(raw), &input) != nil || input.Validate() != nil {
		return nil, errors.New("center: invalid security task")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE node_security_tasks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND attempt=? AND state='pending'`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	return &AgentTask{ID: id, Kind: nodesecurity.Kind, Attempt: attempt + 1, Revision: 1, NodeSecurity: &input}, nil
}

func (s *Store) completeNodeSecurity(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, lease, inputJSON string
	if err = tx.QueryRowContext(ctx, `SELECT state,lease_expires_at,input_json FROM node_security_tasks WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&state, &lease, &inputJSON); err != nil {
		return errStaleTaskLease
	}
	var input nodesecurity.Task
	if json.Unmarshal([]byte(inputJSON), &input) != nil || input.Validate() != nil {
		return errors.New("center: invalid saved security task")
	}
	target := "failed"
	resultJSON := "null"
	if succeeded {
		var result struct {
			NodeSecurity *nodesecurity.View `json:"nodeSecurity"`
		}
		if len(raw) > 64*1024 || json.Unmarshal(raw, &result) != nil || result.NodeSecurity == nil || result.NodeSecurity.ValidateFor(input) != nil {
			return errors.New("center: invalid security result")
		}
		encoded, _ := json.Marshal(result.NodeSecurity)
		resultJSON = string(encoded)
		target = "succeeded"
		taskError = ""
	}
	if state == target {
		return commit(tx)
	}
	expires, e := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || e != nil || !expires.After(s.now()) {
		return errStaleTaskLease
	}
	if len(taskError) > 1024 {
		taskError = "Security operation failed. Inspect the protected host checkpoint."
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_security_tasks SET state=?,error=?,result_json=CASE WHEN ?='null' THEN result_json ELSE ? END,lease_expires_at='',updated_at=? WHERE id=?`, target, taskError, resultJSON, resultJSON, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, nodesecurity.Kind, 1, target, ""); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleNodeSecurity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	v, err := s.store.NodeSecurity(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleQueueNodeSecurity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input nodesecurity.Task
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.store.QueueNodeSecurity(r.Context(), r.PathValue("id"), adminID, input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
