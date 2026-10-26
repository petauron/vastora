package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

// Independent applications do not delegate their business database to Center.
// This is an explicit operator attestation, not an automatic monitor/client
// check. It is accepted only after a matching authenticated replacement install.
// Operators retain the redacted evidence behind the digest outside Center.
type AgentReinstallCurrentVerification struct {
	AgentReinstallApplicationInput
	OriginalIdentity  string    `json:"originalIdentity"`
	EvidenceSHA256    string    `json:"evidenceSha256"`
	RotationReference string    `json:"rotationReference"`
	BusinessReference string    `json:"businessReference"`
	VerifiedAt        time.Time `json:"verifiedAt"`
}

type AgentReinstallCurrentStatus struct {
	DeploymentID string `json:"deploymentId"`
	State        string `json:"state"`
	Verification string `json:"verification"`
}

func (s *Store) currentReinstallProof(ctx context.Context, tx *sql.Tx, target, id string) (ApplicationTaskResult, int, error) {
	var result ApplicationTaskResult
	task, err := s.reinstallCurrentTask(ctx, tx, target, id)
	if err != nil || task == nil {
		return result, 0, errExecutionAuthorization
	}
	var execution string
	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.sealed_result FROM task_executions e JOIN deployments d ON d.id=e.task_id WHERE e.task_id=? AND e.agent_id=? AND e.kind='application.apply' AND e.attempt=1 AND e.state='succeeded' AND e.phase='reported' AND e.identity_retired_at='' AND d.state='succeeded' AND d.reconciliation_required=0`, id, target).Scan(&execution, &sealed)
	if err != nil {
		return result, 0, errors.New("center: authenticated replacement installation is required")
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-result:"+execution))
	if err != nil {
		return result, 0, errExecutionAuthorization
	}
	var proof executionResultEvidence
	if json.Unmarshal(raw, &proof) != nil || !proof.Succeeded || proof.Unknown || proof.ApplicationRuntimeGeneration == nil || *proof.ApplicationRuntimeGeneration < task.RequiredRuntimeGeneration || json.Unmarshal(proof.Result, &result) != nil {
		return result, 0, errExecutionAuthorization
	}
	if task.SenulunRestore != nil && (result.SenulunRestored == nil || result.SenulunRestored.NodeID != task.SenulunRestore.NodeID) {
		return result, 0, errExecutionAuthorization
	}
	if err = validateApplicationResult(task.Manifest, task.AppKey, task.ApplicationRole, task.Config, task.ServiceAddress, result); err != nil {
		return result, 0, err
	}
	return result, *proof.ApplicationRuntimeGeneration, nil
}

func (s *Store) VerifyAgentReinstallCurrent(ctx context.Context, target, admin string, input AgentReinstallCurrentVerification) error {
	digest, err := hex.DecodeString(input.EvidenceSHA256)
	if err != nil || len(digest) != sha256.Size || input.OriginalIdentity == "" || len(input.OriginalIdentity) > 128 || !recoveryReferencePattern.MatchString(input.RotationReference) || !recoveryReferencePattern.MatchString(input.BusinessReference) || input.VerifiedAt.Before(s.now().Add(-15*time.Minute)) || input.VerifiedAt.After(s.now().Add(time.Minute)) {
		return errors.New("center: current operator evidence, original identity and credential/business references are required")
	}
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, err := s.agentReinstallPlan(ctx, tx, target)
	if err != nil {
		return err
	}
	if plan.Recovery == nil || plan.Recovery.ID != input.OperationID || plan.Recovery.AuthorizedBy != admin || plan.Recovery.State != "review_required" || plan.Revision != input.PlanRevision {
		return errExecutionAuthorization
	}
	var id, finished string
	err = tx.QueryRowContext(ctx, `SELECT r.deployment_id,d.updated_at FROM agent_reinstall_current_applications r JOIN deployments d ON d.id=r.deployment_id WHERE r.operation_id=? AND r.application_id=?`, input.OperationID, input.ApplicationID).Scan(&id, &finished)
	if err != nil {
		return err
	}
	completed, err := time.Parse(time.RFC3339Nano, finished)
	if err != nil || input.VerifiedAt.Before(completed) {
		return errors.New("center: verification must follow this replacement installation")
	}
	task, err := s.reinstallCurrentTask(ctx, tx, target, id)
	if err != nil {
		return err
	}
	if task.SenulunRestore != nil && task.SenulunRestore.NodeID != input.OriginalIdentity {
		return errors.New("center: verification does not identify the restored monitoring node")
	}
	if _, _, err = s.currentReinstallProof(ctx, tx, target, id); err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_current_applications SET operator_verification_json=? WHERE deployment_id=?`, raw, id); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, target, "agent.reinstall.current.verify", 1, "succeeded", "Operator recorded original business identity, credential rotation and live access/reporting evidence; this is not an automatic business check"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) requireCurrentReinstallVerification(ctx context.Context, tx *sql.Tx, target, op, app string) (string, ApplicationTaskResult, int, error) {
	var id string
	var raw []byte
	var result ApplicationTaskResult
	err := tx.QueryRowContext(ctx, `SELECT deployment_id,operator_verification_json FROM agent_reinstall_current_applications WHERE operation_id=? AND application_id=?`, op, app).Scan(&id, &raw)
	if err != nil {
		return "", result, 0, errors.New("center: current application restoration and explicit owner verification are required")
	}
	var v AgentReinstallCurrentVerification
	if json.Unmarshal(raw, &v) != nil || v.OperationID != op || v.ApplicationID != app || v.VerifiedAt.Before(s.now().Add(-15*time.Minute)) || v.VerifiedAt.After(s.now().Add(time.Minute)) {
		return "", result, 0, errors.New("center: current operator business verification is required")
	}
	result, generation, err := s.currentReinstallProof(ctx, tx, target, id)
	return id, result, generation, err
}

func (s *Server) handleAgentReinstallCurrentVerification(w http.ResponseWriter, r *http.Request) {
	var input AgentReinstallCurrentVerification
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err = s.store.VerifyAgentReinstallCurrent(r.Context(), r.PathValue("id"), admin, input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"verification": "operator_attested"})
}
