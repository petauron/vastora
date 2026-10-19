package center

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type AgentReinstallCompleteInput struct {
	OperationID  string `json:"operationId"`
	PlanRevision string `json:"planRevision"`
}

type AgentReinstallCompletion struct {
	OperationID         string              `json:"operationId"`
	PlanRevision        string              `json:"planRevision"`
	IdentityFingerprint string              `json:"identityFingerprint"`
	CompletedAt         time.Time           `json:"completedAt"`
	ClientCommands      map[string][]string `json:"clientCommands"`
	RuntimeDigests      map[string]string   `json:"runtimeDigests"`
}

func (s *Store) CompleteAgentReinstall(ctx context.Context, target, admin string, input AgentReinstallCompleteInput) (AgentReinstallCompletion, error) {
	var receipt AgentReinstallCompletion
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var state string
	var saved []byte
	if err = tx.QueryRowContext(ctx, `SELECT state,completion_json FROM agent_reinstall_operations WHERE id=? AND agent_id=? AND authorized_by=? AND EXISTS(SELECT 1 FROM admins WHERE id=?)`, input.OperationID, target, admin, admin).Scan(&state, &saved); err != nil {
		return receipt, errExecutionAuthorization
	}
	if state == "completed" {
		if json.Unmarshal(saved, &receipt) != nil || receipt.OperationID != input.OperationID || receipt.PlanRevision != input.PlanRevision {
			return AgentReinstallCompletion{}, errExecutionAuthorization
		}
		return receipt, nil
	}
	evidence, err := s.requireReinstallCompletion(ctx, tx, target, admin, input.OperationID, input.PlanRevision)
	if err != nil {
		return receipt, err
	}
	receipt = AgentReinstallCompletion{OperationID: input.OperationID, PlanRevision: input.PlanRevision, IdentityFingerprint: evidence.Plan.IdentityFingerprint, CompletedAt: s.now().UTC()}
	stamp := receipt.CompletedAt.Format(time.RFC3339Nano)
	// Restore only the approved generic network after the evidence gate.
	// Application data and route health are never projected by this operation.
	if !evidence.Plan.NetworkReview.ProfileActive {
		profile := evidence.Plan.NetworkReview.Approval.Profile
		profile.ConfirmedAt = receipt.CompletedAt
		profile.CandidateObserved = receipt.CompletedAt
		if err = saveNetworkProfile(ctx, tx, target, profile); err != nil {
			return receipt, err
		}
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET state='completed',completion_json=?,last_error='',updated_at=? WHERE id=? AND agent_id=? AND state='review_required'`, encoded, stamp, input.OperationID, target)
	if err != nil {
		return receipt, err
	}
	if count, _ := changed.RowsAffected(); count != 1 {
		return receipt, errExecutionAuthorization
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, target, "agent.reinstall", 1, "succeeded", "Management recovery completed after identity, network and required application evidence checks"); err != nil {
		return receipt, err
	}
	if err = tx.Commit(); err != nil {
		return AgentReinstallCompletion{}, err
	}
	s.taskChanges.notify("agent:" + target)
	return receipt, nil
}

func (s *Server) handleCompleteAgentReinstall(w http.ResponseWriter, r *http.Request) {
	var input AgentReinstallCompleteInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	receipt, err := s.store.CompleteAgentReinstall(r.Context(), r.PathValue("id"), admin, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, receipt)
}
