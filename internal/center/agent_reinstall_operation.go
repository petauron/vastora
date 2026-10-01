package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

type AgentReinstallInput struct {
	OperationID        string `json:"operationId"`
	PlanRevision       string `json:"planRevision"`
	ConfirmReplacement bool   `json:"confirmReplacement"`
}

type AgentReinstallContinueInput struct {
	OperationID      string `json:"operationId"`
	ExpectedAttempt  int    `json:"expectedAttempt"`
	ConfirmIsolation bool   `json:"confirmIsolation"`
}

type AgentReinstallOperation struct {
	ID                     string `json:"id"`
	PlanRevision           string `json:"planRevision"`
	State                  string `json:"state"`
	PrivateIsolation       string `json:"privateIsolation"`
	Attempt                int    `json:"attempt"`
	AuthorizedBy           string `json:"authorizedBy"`
	PreviousFingerprint    string `json:"previousFingerprint"`
	ReplacementFingerprint string `json:"replacementFingerprint"`
	LastError              string `json:"lastError"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

func readAgentReinstallOperation(ctx context.Context, q networkQueryer, agentID string) (*AgentReinstallOperation, error) {
	var op AgentReinstallOperation
	err := q.QueryRowContext(ctx, `SELECT id,plan_revision,state,private_isolation,attempt,authorized_by,previous_fingerprint,replacement_fingerprint,last_error,created_at,updated_at
 FROM agent_reinstall_operations WHERE agent_id=? AND state NOT IN ('superseded','completed')`, agentID).
		Scan(&op.ID, &op.PlanRevision, &op.State, &op.PrivateIsolation, &op.Attempt, &op.AuthorizedBy, &op.PreviousFingerprint, &op.ReplacementFingerprint, &op.LastError, &op.CreatedAt, &op.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &op, err
}

func agentReinstallBlocked(ctx context.Context, q networkQueryer, agentID string) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_operations WHERE agent_id=? AND state NOT IN ('superseded','completed'))`, agentID).Scan(&blocked)
	return blocked, err
}

func (s *Store) CreateAgentReconnectEnrollment(ctx context.Context, agentID, adminID string, input AgentReinstallInput) (AgentEnrollment, error) {
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	cached, err := s.beginAgentReinstall(ctx, strings.TrimSpace(agentID), adminID, input)
	if err != nil {
		return AgentEnrollment{}, err
	}
	if cached != nil {
		return *cached, nil
	}
	return s.prepareAgentReinstallEnrollment(ctx, agentID, input.OperationID)
}

// Only an explicit action using the inspected attempt can continue interrupted
// identity isolation. Never resume bootstrap creation with an uncertain result.
func (s *Store) ContinueAgentReinstallIsolation(ctx context.Context, agentID, adminID string, input AgentReinstallContinueInput) (AgentEnrollment, error) {
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	if !input.ConfirmIsolation || input.ExpectedAttempt < 1 {
		return AgentEnrollment{}, errors.New("center: explicitly confirm private identity inspection before continuing")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_reinstall_operations SET state='preparing',attempt=attempt+1,last_error='',updated_at=?
 WHERE id=? AND agent_id=? AND authorized_by=? AND attempt=? AND state IN ('preparing','failed') AND private_isolation='pending'
 AND EXISTS(SELECT 1 FROM admins WHERE id=?) AND EXISTS(SELECT 1 FROM agents WHERE id=? AND credential_revoked_at<>'')`, s.now().UTC().Format(time.RFC3339Nano), input.OperationID, agentID, adminID, input.ExpectedAttempt, adminID, agentID)
	if err != nil {
		return AgentEnrollment{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return AgentEnrollment{}, errors.New("center: recovery progress changed or command preparation already started; refresh before continuing")
	}
	return s.prepareAgentReinstallEnrollment(ctx, agentID, input.OperationID)
}

func (s *Store) prepareAgentReinstallEnrollment(ctx context.Context, agentID, operationID string) (AgentEnrollment, error) {
	err := s.isolateReinstallPrivateIdentity(ctx, agentID, operationID)
	message := ""
	var enrollment AgentEnrollment
	if err != nil {
		message = err.Error()
	} else {
		enrollment, err = s.createAgentReconnectEnrollment(ctx, agentID, operationID)
		message = "center: command preparation stopped; inspect recovery before continuing"
	}
	if err != nil {
		// A lost external response is not permission to replay bootstrap creation.
		// Keep the persistent fence, including when the request context was cancelled.
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, saveErr := s.db.ExecContext(failureCtx, `UPDATE agent_reinstall_operations SET state='failed',last_error=?,updated_at=? WHERE id=? AND state='preparing'`, message, s.now().UTC().Format(time.RFC3339Nano), operationID)
		if saveErr != nil {
			return AgentEnrollment{}, errors.Join(err, saveErr)
		}
	}
	return enrollment, err
}

// Reserve and fence before any external bootstrap call. An interrupted preparing
// operation remains visible and cannot silently repeat a remote side effect.
func (s *Store) beginAgentReinstall(ctx context.Context, agentID, adminID string, input AgentReinstallInput) (*AgentEnrollment, error) {
	if !input.ConfirmReplacement || strings.TrimSpace(adminID) == "" || len(input.OperationID) < 16 || len(input.OperationID) > 128 || strings.TrimSpace(input.OperationID) != input.OperationID || len(input.PlanRevision) != 64 {
		return nil, errors.New("center: review the recovery plan and explicitly confirm identity replacement")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var admin bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return nil, err
	}
	if !admin {
		return nil, errors.New("center: administrator authorization required")
	}
	var storedAgent, storedAdmin, revision, state string
	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT agent_id,authorized_by,plan_revision,state,sealed_enrollment FROM agent_reinstall_operations WHERE id=?`, input.OperationID).Scan(&storedAgent, &storedAdmin, &revision, &state, &sealed)
	if err == nil {
		if storedAgent != agentID || storedAdmin != adminID || revision != input.PlanRevision {
			return nil, errors.New("center: recovery operation does not match this authorization")
		}
		if state != "awaiting_enrollment" {
			return nil, errors.New("center: recovery already started; inspect its saved progress before continuing")
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_operations r JOIN agent_enrollment_tokens t ON t.token_hash=r.enrollment_token_hash
   JOIN agents a ON a.id=r.agent_id WHERE r.id=? AND t.used_at IS NULL AND t.expires_at>? AND a.credential_revoked_at<>'')`, input.OperationID, s.now().UTC().Format(time.RFC3339Nano)).Scan(&valid)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, errors.New("center: recovery command is no longer valid; review the node again")
		}
		encoded, err := secret.Open(s.key, sealed, []byte("agent-reinstall:"+input.OperationID))
		if err != nil {
			return nil, err
		}
		var enrollment AgentEnrollment
		if json.Unmarshal(encoded, &enrollment) != nil || enrollment.Token == "" {
			return nil, errors.New("center: stored recovery command is invalid")
		}
		return &enrollment, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if plan.Revision != input.PlanRevision {
		return nil, errors.New("center: recovery plan changed; review it again before confirming")
	}
	var status, lastSeen, revoked string
	var removing bool
	if err = tx.QueryRowContext(ctx, `SELECT status,last_seen_at,credential_revoked_at,
 EXISTS(SELECT 1 FROM agent_removals WHERE agent_id=agents.id) OR EXISTS(SELECT 1 FROM agent_decommissions WHERE agent_id=agents.id AND state IN ('pending','running','cleaning'))
 FROM agents WHERE id=?`, agentID).Scan(&status, &lastSeen, &revoked, &removing); err != nil {
		return nil, err
	}
	seen, err := time.Parse(time.RFC3339Nano, lastSeen)
	if err != nil {
		return nil, errors.New("center: stored Agent heartbeat time is invalid")
	}
	if status != "active" && status != "disabled" || removing {
		return nil, errors.New("center: Agent removal must finish before reconnecting")
	}
	if revoked == "" && seen.After(s.now().UTC().Add(-agentConnectedMaxAge)) {
		return nil, errors.New("center: disconnect the Agent before generating a reconnect command")
	}
	if plan.Recovery != nil && plan.Recovery.State != "awaiting_enrollment" {
		return nil, errors.New("center: recovery already started; inspect its saved progress before continuing")
	}
	privateIdentity, err := captureReinstallPrivateIdentity(ctx, tx, agentID, plan.PrivateNetwork)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	// Only an explicit, fresh review may replace an unused grant. The operation
	// history remains; retrying the same request ID above returns the same command.
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET state='superseded',sealed_enrollment=NULL,updated_at=? WHERE agent_id=? AND state='awaiting_enrollment'`, now, agentID); err != nil {
		return nil, err
	}
	plan.Recovery = nil
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_operations(id,agent_id,authorized_by,plan_revision,plan_json,previous_fingerprint,private_identity_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'preparing',?,?)`, input.OperationID, agentID, adminID, input.PlanRevision, encoded, plan.IdentityFingerprint, privateIdentity, now, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE target_agent_id=? AND bootstrap_secret_id IS NOT NULL)`, agentID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE target_agent_id=?`, agentID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET credential_revoked_at=? WHERE id=?`, now, agentID); err != nil {
		return nil, err
	}
	if err = retireAgentExecutionIdentity(ctx, tx, agentID, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return nil, nil
}

func (s *Store) saveAgentReinstallEnrollment(ctx context.Context, tx *sql.Tx, agentID, operationID string, enrollment AgentEnrollment, now string) error {
	encoded, err := json.Marshal(enrollment)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, []byte("agent-reinstall:"+operationID))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET state='awaiting_enrollment',enrollment_token_hash=?,sealed_enrollment=?,updated_at=? WHERE id=? AND agent_id=? AND state='preparing' AND private_isolation IN ('not_required','withdrawn')`, tokenHash(enrollment.Token), sealed, now, operationID, agentID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("center: recovery command authorization changed")
	}
	return nil
}

func acceptAgentReinstallIdentity(ctx context.Context, tx *sql.Tx, agentID string, tokenHash, publicKey []byte, now string) error {
	digest := sha256.Sum256(publicKey)
	fingerprint := hex.EncodeToString(digest[:])
	result, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET state='review_required',replacement_fingerprint=?,sealed_enrollment=NULL,updated_at=?
 WHERE agent_id=? AND enrollment_token_hash=? AND state='awaiting_enrollment' AND previous_fingerprint<>?`, fingerprint, now, agentID, tokenHash, fingerprint)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("center: replacement requires an authorized recovery command and a new machine key")
	}
	// Old-machine address observations cannot become fresh merely because the
	// replacement enrollment updated last_seen_at.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE agent_id=?`, agentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_private_peer_capabilities WHERE node_id=?`, agentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET public_egress_address='',public_egress_bind_address='',public_egress_mode='',public_egress_observed_at='' WHERE id=?`, agentID); err != nil {
		return err
	}
	// Save the old binding while its old public key still exists. Otherwise a
	// heartbeat could accidentally attribute the old profile to the new machine.
	profile, err := networkProfile(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if profile != nil {
		encoded, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_network_profile_recovery(agent_id,public_key,profile_json) SELECT id,x25519_public_key,? FROM agents WHERE id=?
   ON CONFLICT(agent_id) DO UPDATE SET public_key=excluded.public_key,profile_json=excluded.profile_json`, encoded, agentID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM agent_network_profiles WHERE agent_id=?`, agentID); err != nil {
			return err
		}
	}
	return nil
}
