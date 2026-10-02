package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

type AgentReinstallAccess struct {
	State          string    `json:"state"`
	ServiceAddress string    `json:"serviceAddress"`
	PublicAddress  string    `json:"publicAddress"`
	ActivatedAt    time.Time `json:"activatedAt"`
}

// Check actual restored task receipts, not application status inherited from
// the previous machine. No endpoint or landing health is inferred here.
func (s *Store) reinstallAccessTarget(ctx context.Context, tx *sql.Tx, agentID, preparationID string) (string, *AgentTask, error) {
	var runtimeID, listenerID, listenerState, serviceID string
	var shared, listenerReady bool
	err := tx.QueryRowContext(ctx, `SELECT p.runtime_command_id,COALESCE(p.listener_task_id,''),p.listener_state,e.service_id,
 EXISTS(SELECT 1 FROM publications pub WHERE pub.service_id=e.service_id AND pub.kind='public_shared_443' AND pub.status<>'stopped'),
 COALESCE(n.status='ready' AND n.desired_revision=p.listener_revision AND n.applied_revision=p.listener_revision AND n.attempt=p.listener_attempt,0)
 FROM agent_reinstall_app_preparations p JOIN application_commands c ON c.id=p.runtime_command_id
 JOIN meridian_endpoints e ON e.id=p.runtime_endpoint_id JOIN services service ON service.id=e.service_id
 LEFT JOIN node_listener_states n ON n.node_id=?
 WHERE p.deployment_id=? AND c.state='succeeded' AND service.application_id=p.application_id AND service.status<>'stopped'`, agentID, preparationID).Scan(&runtimeID, &listenerID, &listenerState, &serviceID, &shared, &listenerReady)
	if err != nil {
		return "", nil, errExecutionAuthorization
	}
	task, err := s.reinstallRuntimeTask(ctx, tx, agentID, runtimeID, false)
	if err != nil {
		return "", nil, err
	}
	if task == nil {
		return "", nil, errExecutionAuthorization
	}
	if shared || listenerID != "" {
		if listenerState != "succeeded" || !listenerReady {
			return "", nil, errors.New("center: restore the saved entry before activating its address")
		}
		if listener, err := s.reinstallListenerTask(ctx, tx, agentID, listenerID, false); err != nil {
			return "", nil, err
		} else if listener == nil {
			return "", nil, errExecutionAuthorization
		}
	}
	return serviceID, task, nil
}

func (s *Store) readReinstallAccess(ctx context.Context, tx *sql.Tx, agentID, preparationID string) (*AgentReinstallAccess, error) {
	var result AgentReinstallAccess
	var encoded []byte
	var activated string
	var matches bool
	err := tx.QueryRowContext(ctx, `SELECT a.profile_json,a.activated_at,
 s.id=a.service_id AND s.endpoint=a.service_endpoint AND s.observed_listen=json_extract(a.profile_json,'$.serviceAddress') AND s.status<>'stopped'
 AND e.listen_address=json_extract(a.profile_json,'$.serviceAddress')
 FROM agent_reinstall_access_activations a JOIN agent_reinstall_app_preparations p ON p.deployment_id=a.preparation_id
 JOIN services s ON s.id=a.service_id JOIN meridian_endpoints e ON e.id=p.runtime_endpoint_id
 WHERE a.preparation_id=?`, preparationID).Scan(&encoded, &activated, &matches)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var expected networking.Profile
	if err = json.Unmarshal(encoded, &expected); err != nil {
		return nil, err
	}
	result.ServiceAddress, result.PublicAddress = expected.ServiceAddress, expected.PublicAddress
	result.ActivatedAt, err = time.Parse(time.RFC3339Nano, activated)
	if err != nil {
		return nil, err
	}
	result.State = "needs_review"
	actual, err := networkProfile(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if matches && actual != nil && reflect.DeepEqual(normalizeReinstallProfile(*actual), normalizeReinstallProfile(expected)) {
		if _, _, err = s.reinstallAccessTarget(ctx, tx, agentID, preparationID); err == nil {
			result.State = "applied"
		}
	}
	return &result, nil
}

func (s *Store) ActivateAgentReinstallAccess(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput) (AgentReinstallAccess, error) {
	var result AgentReinstallAccess
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	// Existing external DNS operations finish before the replacement profile
	// becomes visible. Subsequent ordinary DNS reconciliation remains fenced.
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var preparationID, endpointID string
	var approvalJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT p.deployment_id,p.runtime_endpoint_id,p.approval_json
 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id
 WHERE op.id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required' AND p.application_id=?`, input.OperationID, agentID, adminID, input.ApplicationID).Scan(&preparationID, &endpointID, &approvalJSON)
	if err != nil {
		return result, errExecutionAuthorization
	}
	serviceID, task, err := s.reinstallAccessTarget(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	saved, err := s.readReinstallAccess(ctx, tx, agentID, preparationID)
	if err != nil {
		return result, err
	}
	if saved != nil {
		// A lost response only retrieves the same durable receipt. Changed bindings
		// require a separate review; this endpoint never silently reapplies them.
		if saved.State != "applied" {
			return result, errors.New("center: activated recovery addresses changed; inspect before continuing")
		}
		return *saved, nil
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if len(input.PlanRevision) != 64 || plan.Revision != input.PlanRevision {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return result, errors.New("center: settle previous work before activating recovery addresses")
	}
	var approval AgentReinstallNetworkApproval
	if json.Unmarshal(approvalJSON, &approval) != nil {
		return result, errExecutionAuthorization
	}
	if plan.NetworkReview == nil || !plan.NetworkReview.ApprovalCurrent {
		return result, errExecutionAuthorization
	}
	profile, err := validateReinstallProfile(plan.NetworkReview, normalizeReinstallProfile(approval.Profile))
	if err != nil {
		return result, err
	}
	active, err := networkProfile(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	if active != nil && !reflect.DeepEqual(normalizeReinstallProfile(*active), normalizeReinstallProfile(profile)) {
		return result, errors.New("center: active network differs from the reviewed replacement address")
	}
	now := s.now().UTC()
	profile.ConfirmedAt = now
	profile.CandidateObserved = now
	if err = saveNetworkProfile(ctx, tx, agentID, profile); err != nil {
		return result, err
	}
	var port int
	if err = tx.QueryRowContext(ctx, `SELECT listen_port FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&port); err != nil {
		return result, err
	}
	endpoint := net.JoinHostPort(profile.ServiceAddress, fmt.Sprint(port))
	if _, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET listen_address=?,updated_at=? WHERE id=?`, profile.ServiceAddress, now.Format(time.RFC3339Nano), endpointID); err != nil {
		return result, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE services SET endpoint=?,observed_listen=?,container_port=?,host_port=?,protocol='tcp',app_protocol=?,status='degraded',last_error='Reinstall recovery: client access verification pending',updated_at=? WHERE id=? AND application_id=? AND status<>'stopped'`, endpoint, profile.ServiceAddress, port, port, meridianEntryProtocol, now.Format(time.RFC3339Nano), serviceID, input.ApplicationID)
	if err != nil {
		return result, err
	}
	if n, _ := update.RowsAffected(); n != 1 {
		return result, errExecutionAuthorization
	}
	if _, err = tx.ExecContext(ctx, `UPDATE applications SET image=?,status='running',updated_at=? WHERE id=? AND node_id=?`, task.MeridianRuntime.ImageReference, now.Format(time.RFC3339Nano), input.ApplicationID, agentID); err != nil {
		return result, err
	}
	// The address projection must still compile to the exact task already applied.
	if _, _, err = s.reinstallAccessTarget(ctx, tx, agentID, preparationID); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_access_activations(preparation_id,plan_revision,profile_json,service_id,service_endpoint,activated_at) VALUES(?,?,?,?,?,?)`, preparationID, input.PlanRevision, encoded, serviceID, endpoint, now.Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, preparationID); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, agentID, "agent.reinstall.access", 0, "succeeded", "Reviewed replacement addresses activated; DNS, landing authorization and client access remain under recovery"); err != nil {
		return result, err
	}
	result = AgentReinstallAccess{State: "applied", ServiceAddress: profile.ServiceAddress, PublicAddress: profile.PublicAddress, ActivatedAt: now}
	return result, tx.Commit()
}

func (s *Server) handleAgentReinstallAccess(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallApplicationInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.ActivateAgentReinstallAccess(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
