package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/meridian"
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
	type readyRuntime struct {
		endpoint, command string
		projection        meridianRuntimeProjection
		quota             bool
	}
	runtimes := []readyRuntime{}
	receipt = AgentReinstallCompletion{OperationID: input.OperationID, PlanRevision: input.PlanRevision, IdentityFingerprint: evidence.Plan.IdentityFingerprint, CompletedAt: s.now().UTC(), ClientCommands: evidence.Clients, RuntimeDigests: map[string]string{}}
	stamp := receipt.CompletedAt.Format(time.RFC3339Nano)
	// Meridian activates its reviewed profile before client verification. Nodes
	// without Meridian still need the same approved network restored atomically
	// with completion, after the full evidence gate has succeeded.
	if !evidence.Plan.NetworkReview.ProfileActive {
		profile := evidence.Plan.NetworkReview.Approval.Profile
		profile.ConfirmedAt = receipt.CompletedAt
		profile.CandidateObserved = receipt.CompletedAt
		if err = saveNetworkProfile(ctx, tx, target, profile); err != nil {
			return receipt, err
		}
	}
	for _, app := range evidence.Plan.Applications {
		result, exists := evidence.Runtimes[app.ApplicationID]
		if !exists {
			continue
		}
		p := app.Preparation
		var endpoint string
		if err = tx.QueryRowContext(ctx, `SELECT runtime_endpoint_id FROM agent_reinstall_app_preparations WHERE deployment_id=?`, p.DeploymentID).Scan(&endpoint); err != nil {
			return receipt, err
		}
		_, task, err := s.reinstallAccessTarget(ctx, tx, target, p.DeploymentID)
		if err != nil {
			return receipt, err
		}
		restore := &meridianRuntimeRestoreTarget{Address: p.Access.ServiceAddress, Image: task.MeridianRuntime.ImageReference, Revision: task.MeridianRuntime.Desired.Revision, RestoreLandings: p.Landing != nil}
		projection, err := s.buildMeridianRuntimeProjection(ctx, tx, endpoint, target, restore)
		if err != nil {
			return receipt, err
		}
		if projection.task.Desired.ConfigSHA256 != task.MeridianRuntime.Desired.ConfigSHA256 {
			return receipt, errExecutionAuthorization
		}
		// Apply real counters once using existing watermarks. If they change quota
		// intent, stop rather than mark a now-obsolete verified configuration ready.
		counters, err := meridian.ParseXrayUserCounters(projection.materials, result.Stats)
		if err != nil {
			return receipt, err
		}
		if err = s.observeMeridianUsageInTx(ctx, tx, endpoint, counters, stamp); err != nil {
			return receipt, err
		}
		after, err := s.buildMeridianRuntimeProjection(ctx, tx, endpoint, target, restore)
		if err != nil || after.task.Desired.ConfigSHA256 != projection.task.Desired.ConfigSHA256 {
			return receipt, errors.New("center: usage changed restored access requirements; review runtime before completion")
		}
		quota, err := meridianEndpointQuotaEnabledInTx(ctx, tx, endpoint)
		if err != nil {
			return receipt, err
		}
		runtimes = append(runtimes, readyRuntime{endpoint: endpoint, command: p.Runtime.CommandID, projection: projection, quota: quota})
		receipt.RuntimeDigests[app.ApplicationID] = projection.task.Desired.ConfigSHA256
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
	// The fence is lifted inside this transaction only. Any failed projection
	// rolls back both completion and every business-state update.
	for _, ready := range runtimes {
		task := ready.projection.task
		if _, err = tx.ExecContext(ctx, `INSERT INTO meridian_deployments(endpoint_id,desired_revision,desired_sha256,command_id,status,updated_at) VALUES(?,?,?,?,'applying',?) ON CONFLICT(endpoint_id) DO UPDATE SET desired_revision=excluded.desired_revision,desired_sha256=excluded.desired_sha256,command_id=excluded.command_id,status='applying',updated_at=excluded.updated_at`, ready.endpoint, task.Desired.Revision, task.Desired.ConfigSHA256, ready.command, stamp); err != nil {
			return receipt, err
		}
		result := evidence.Runtimes[task.ApplicationID]
		if err = s.projectVerifiedMeridianRuntime(ctx, tx, ready.endpoint, ready.command, ready.projection, &result, ready.quota, receipt.CompletedAt); err != nil {
			return receipt, err
		}
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, target, "agent.reinstall", 1, "succeeded", "Recovery completed after authenticated original-client and monitoring verification"); err != nil {
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
