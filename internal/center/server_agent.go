package center

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

func (s *Server) handleStoreThreeXUIBackup(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	revision, err := strconv.ParseInt(request.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		writeError(writer, http.StatusBadRequest, errors.New("center: invalid restore point revision"))
		return
	}
	value, err := s.store.StoreThreeXUIBackup(request.Context(), request.PathValue("id"), credential, request.PathValue("applicationID"), revision, request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, value)
}

func (s *Server) handleThreeXUIMigrationBackup(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	value, err := s.store.ThreeXUIMigrationBackup(request.Context(), request.PathValue("id"), credential, request.PathValue("migrationID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Length", strconv.Itoa(len(value)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(value)
}

func (s *Server) handleListAgents(writer http.ResponseWriter, request *http.Request) {
	agents, err := s.store.ListAgents(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleCreateAgentEnrollment(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SiteID           string `json:"siteId"`
		Name             string `json:"name"`
		CenterURL        string `json:"centerUrl"`
		UseHeadscale     bool   `json:"useHeadscale"`
		Gateway          bool   `json:"gateway"`
		Tunnel           bool   `json:"tunnel"`
		CAFingerprint    string `json:"caFingerprint"`
		CACertificatePEM string `json:"caCertificatePem"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.UseHeadscale && s.coLocatedAgentURL != "" {
		var agentCount int
		if err := s.store.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM agents`).Scan(&agentCount); err != nil {
			writeError(writer, http.StatusInternalServerError, fmt.Errorf("center: inspect existing Agents: %w", err))
			return
		}
		if agentCount == 0 {
			input.CenterURL = s.coLocatedAgentURL
			input.CAFingerprint = ""
			input.CACertificatePEM = ""
		}
	}
	enrollment, err := s.store.CreateAgentEnrollment(request.Context(), AgentEnrollmentSpec{
		SiteID: input.SiteID, Name: input.Name, CenterURL: input.CenterURL,
		UseHeadscale: input.UseHeadscale, Gateway: input.Gateway, Tunnel: input.Tunnel,
		CAFingerprint: input.CAFingerprint, CACertificatePEM: input.CACertificatePEM,
	})
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, enrollment)
}

func (s *Server) handleCreateAgentReconnectEnrollment(writer http.ResponseWriter, request *http.Request) {
	enrollment, err := s.store.CreateAgentReconnectEnrollment(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, enrollment)
}

func (s *Server) handleUpdateAgent(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SiteID string `json:"siteId"`
		Name   string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdateAgent(request.Context(), request.PathValue("id"), input.Name, input.SiteID); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"updated": true})
}

func (s *Server) handleDeleteAgent(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DeleteAgent(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleDisableAgent(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DisableAgent(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"disabled": true})
}

func (s *Server) handleRemoveOfflineAgent(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.StartAgentRemoval(request.Context(), request.PathValue("id"), input.Confirmation); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"removing": true})
}

func (s *Server) handleRevokeAgentCredential(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.RevokeAgentCredential(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"revoked": true})
}

func (s *Server) handleConfirmNetworkProfile(writer http.ResponseWriter, request *http.Request) {
	var input networking.Profile
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	profile, err := s.store.ConfirmNetworkProfile(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

func (s *Server) handleEnrollAgent(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Token           string `json:"token"`
		OperationID     string `json:"operationId"`
		Version         string `json:"version"`
		OperatingSystem string `json:"operatingSystem"`
		Architecture    string `json:"architecture"`
		PublicKey       []byte `json:"publicKey"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.EnrollAgentOperation(request.Context(), input.Token, input.OperationID, input.Version, input.OperatingSystem, input.Architecture, input.PublicKey)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusCreated, credential)
}

func (s *Server) handleAgentHeartbeat(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		PublicKey                    []byte                             `json:"publicKey"`
		Version                      string                             `json:"version"`
		AppliedInstallations         int                                `json:"appliedInstallations"`
		Roles                        []string                           `json:"roles"`
		Capabilities                 NodeCapabilities                   `json:"capabilities"`
		NetworkCandidates            []networking.Candidate             `json:"networkCandidates"`
		PublicEgress                 *networking.PublicEgress           `json:"publicEgress"`
		ApplicationEndpoints         []ApplicationEndpointObservation   `json:"applicationEndpoints"`
		ApplicationEndpointsObserved bool                               `json:"applicationEndpointsObserved"`
		MeridianRuntime              *meridianruntime.Result            `json:"meridianRuntime"`
		GatewayHealthy               bool                               `json:"gatewayHealthy"`
		RuntimeRecovery              string                             `json:"runtimeRecovery"`
		RuntimeRecoveryApplications  []controlplane.RecoveryApplication `json:"runtimeRecoveryApplications"`
		GatewayRevision              int64                              `json:"gatewayRevision"`
		GatewayConfigHash            string                             `json:"gatewayConfigHash"`
		NodeListenerHealthy          bool                               `json:"nodeListenerHealthy"`
		LandingHealth                *landing.Health                    `json:"landingHealth"`
		LandingClientRuntime         *landing.ClientRuntime             `json:"landingClientRuntime"`
		NodeListenerRevision         int64                              `json:"nodeListenerRevision"`
		NodeListenerConfigHash       string                             `json:"nodeListenerConfigHash"`
		ApplicationRuntimeGeneration int                                `json:"applicationRuntimeGeneration"`
		RemoteUpdateSupported        bool                               `json:"remoteUpdateSupported"`
		TailscaleEnrolled            bool                               `json:"tailscaleEnrolled"`
		TailscaleOwnership           string                             `json:"tailscaleOwnership"`
		Startup                      bool                               `json:"startup"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if credential == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		writeError(writer, http.StatusUnauthorized, errors.New("center: agent authentication required"))
		return
	}
	if err := s.store.authenticateAgent(request.Context(), request.PathValue("id"), credential); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err := s.store.RecordAgentHeartbeat(request.Context(), request.PathValue("id"), credential, NodeHeartbeat{LandingClientRuntime: input.LandingClientRuntime, LandingHealth: input.LandingHealth, PublicKey: input.PublicKey, Version: input.Version, AppliedInstallations: input.AppliedInstallations, Roles: input.Roles, Capabilities: input.Capabilities, NetworkCandidates: input.NetworkCandidates, PublicEgress: input.PublicEgress, ApplicationEndpoints: input.ApplicationEndpoints, ApplicationEndpointsObserved: input.ApplicationEndpointsObserved, MeridianRuntime: input.MeridianRuntime, GatewayHealthy: input.GatewayHealthy, RuntimeRecovery: input.RuntimeRecovery, RuntimeRecoveryApplications: input.RuntimeRecoveryApplications, GatewayRevision: input.GatewayRevision, GatewayConfigHash: input.GatewayConfigHash, NodeListenerHealthy: input.NodeListenerHealthy, NodeListenerRevision: input.NodeListenerRevision, NodeListenerConfigHash: input.NodeListenerConfigHash, ApplicationRuntimeGeneration: input.ApplicationRuntimeGeneration, RemoteUpdateSupported: input.RemoteUpdateSupported, TailscaleOwnership: input.TailscaleOwnership, Startup: input.Startup}); err != nil {
		slog.ErrorContext(request.Context(), "Agent heartbeat projection failed", "agent_id", request.PathValue("id"), "error", controlplane.SafeError(err.Error()))
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	targets, err := s.store.landingLatencyTargets(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	network, err := s.store.CenterNetworkConfig(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	var isolation *TailscaleIsolationDesiredState
	if input.TailscaleEnrolled {
		isolation, err = s.store.tailscaleIsolationDesiredState(request.Context(), request.PathValue("id"))
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
	}
	centerURL := network.AgentConnectURL
	if s.coLocatedAgentURL != "" {
		coLocated, err := s.store.networkCandidatesAreCoLocated(input.NetworkCandidates)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		if coLocated {
			centerURL = s.coLocatedAgentURL
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"connected":                true,
		"landingLatencyTargets":    targets,
		"centerUrl":                centerURL,
		"tailscaleIsolation":       isolation,
		"publicAddressLookupUrl":   s.publicAddressLookupURL,
		"publicHelperAllowPrivate": s.publicHelperAllowPrivate,
	})
}

func (s *Server) handleClaimTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err := s.store.authenticateAgent(request.Context(), request.PathValue("id"), credential); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	sessionID := request.Header.Get("X-Vastora-Execution-Session")
	if err := s.store.executionClaimAllowed(request.Context(), request.PathValue("id"), sessionID); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	wait := time.Duration(0)
	if value := strings.TrimSpace(request.URL.Query().Get("wait")); value != "" {
		wait, err = time.ParseDuration(value)
		if err != nil || wait < 0 || wait > 30*time.Second {
			writeError(writer, http.StatusBadRequest, errors.New("center: task wait must be between 0 and 30 seconds"))
			return
		}
	}
	for key, values := range request.URL.Query() {
		if key != "wait" || len(values) != 1 {
			writeError(writer, http.StatusBadRequest, errors.New("center: unsupported task claim parameter"))
			return
		}
	}
	task, err := s.store.claimExecutionTask(request.Context(), request.PathValue("id"), credential, sessionID, wait)
	if err != nil {
		if errors.Is(err, errExecutionBlocked) || errors.Is(err, errExecutionAuthorization) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		slog.ErrorContext(request.Context(), "Agent task claim failed", "agent_id", request.PathValue("id"), "error", controlplane.SafeError(err.Error()))
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"task": nil})
		return
	}
	if task.Kind == "landing.server.apply" || task.Kind == "landing.proxy.apply" && task.LandingProxyState != nil && task.LandingProxyState.Active() {
		if err := s.syncLandingAccess(request.Context()); err != nil {
			// Keep the offered execution fenced: the access change may already
			// have reached an external service. Never put it back in the queue.
			writeError(writer, http.StatusServiceUnavailable, err)
			return
		}
	}
	encrypted, err := s.store.EncryptAgentTask(request.Context(), request.PathValue("id"), *task)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	encrypted.Authorization = task.Authorization
	writeJSON(writer, http.StatusOK, map[string]any{"task": encrypted})
}

func (s *Server) handleCompleteTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		ExecutionID                  string          `json:"executionId"`
		SessionID                    string          `json:"sessionId"`
		Attempt                      int64           `json:"attempt"`
		Succeeded                    bool            `json:"succeeded"`
		Error                        string          `json:"error"`
		Result                       json.RawMessage `json:"result"`
		ReconciliationRequired       bool            `json:"reconciliationRequired"`
		ApplicationRuntimeGeneration *int            `json:"applicationRuntimeGeneration"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	agentID := request.PathValue("id")
	if err := s.store.authenticateAgent(request.Context(), agentID, credential); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var matches bool
	if err := s.store.db.QueryRowContext(request.Context(), `SELECT EXISTS(SELECT 1 FROM task_executions WHERE id=? AND agent_id=? AND task_id=? AND attempt=? AND session_id=?)`, input.ExecutionID, agentID, request.PathValue("taskID"), input.Attempt, input.SessionID).Scan(&matches); err != nil || !matches {
		writeError(writer, http.StatusConflict, errExecutionAuthorization)
		return
	}
	if len(input.Result) == 0 {
		input.Result = json.RawMessage(`{}`)
	}
	if err := s.store.StoreExecutionResult(request.Context(), agentID, input.SessionID, input.ExecutionID, input.Result, input.Succeeded, input.ReconciliationRequired, input.Error, input.ApplicationRuntimeGeneration); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	executedRuntimeGenerations := []int{}
	if input.ApplicationRuntimeGeneration != nil {
		executedRuntimeGenerations = append(executedRuntimeGenerations, *input.ApplicationRuntimeGeneration)
	}
	committed := false
	commit := func(tx *sql.Tx) error {
		if err := validateExecutionProjection(request.Context(), tx, input.ExecutionID, input.Succeeded); err != nil {
			return err
		}
		if err := s.store.finalizeExecution(request.Context(), tx, agentID, input.SessionID, input.ExecutionID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err := s.store.completeTaskWithDisposition(request.Context(), commit, request.PathValue("id"), credential, request.PathValue("taskID"), input.Attempt, input.Succeeded, input.Error, input.Result, input.ReconciliationRequired, executedRuntimeGenerations...); err != nil {
		slog.ErrorContext(request.Context(), "Task result projection failed", "execution_id", input.ExecutionID, "task_id", request.PathValue("taskID"), "error", err)
		if errors.Is(err, errInvalidReconciliationDisposition) {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writeError(writer, http.StatusConflict, err)
		return
	}
	if !committed {
		writeError(writer, http.StatusConflict, errExecutionAuthorization)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"completed": true})
}

func (s *Server) handleCompleteAgentDecommissionCallback(writer http.ResponseWriter, request *http.Request) {
	token, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		Action   string `json:"action,omitempty"`
		Sequence int64  `json:"sequence,omitempty"`
		Phase    string `json:"phase,omitempty"`
		Attempt  int64  `json:"attempt"`
		Error    string `json:"error,omitempty"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Action == "step" {
		if input.Error != "" {
			writeError(writer, http.StatusBadRequest, errExecutionAuthorization)
			return
		}
		if err := s.store.AuthorizeDecommissionStep(request.Context(), request.PathValue("taskID"), token, input.Attempt, input.Sequence, input.Phase); err != nil {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeJSON(writer, http.StatusOK, map[string]bool{"recorded": true})
		return
	}
	if input.Action != "" || input.Sequence != 0 || input.Phase != "" {
		writeError(writer, http.StatusBadRequest, errExecutionAuthorization)
		return
	}
	if err := s.store.completeAgentDecommissionCallback(request.Context(), request.PathValue("taskID"), token, input.Attempt, input.Error); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"completed": input.Error == "", "recorded": true})
}

func (s *Server) handleStartAgentDecommission(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		ExecutionID string `json:"executionId"`
		SessionID   string `json:"sessionId"`
		TaskID      string `json:"taskId"`
		Attempt     int64  `json:"attempt"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.beginAgentDecommission(request.Context(), request.PathValue("id"), credential, input.TaskID, input.Attempt, input.ExecutionID, input.SessionID); err != nil {
		if errors.Is(err, errStaleTaskLease) || errors.Is(err, errExecutionAuthorization) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"started": true})
}

func (s *Server) handleQueueAgentUpdate(writer http.ResponseWriter, request *http.Request) {
	update, err := s.store.QueueAgentUpdate(request.Context(), request.PathValue("id"), Version)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, update)
}

func (s *Server) handleRecoverAgentUpdate(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input AgentUpdateRecoveryInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.adminID = adminID
	update, err := s.store.queueAgentUpdate(request.Context(), request.PathValue("id"), Version, &input)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, update)
}

func (s *Server) handleBeginAgentUpdate(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		ExecutionID string `json:"executionId"`
		SessionID   string `json:"sessionId"`
		Attempt     int64  `json:"attempt"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.beginAgentUpdateExecution(request.Context(), request.PathValue("id"), credential, request.PathValue("taskID"), input.Attempt, input.ExecutionID, input.SessionID); err != nil {
		if errors.Is(err, errStaleTaskLease) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"started": true})
}

func (s *Server) handleRenewTaskLease(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		Attempt int64 `json:"attempt"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	expiresAt, err := s.store.RenewTaskLease(request.Context(), request.PathValue("id"), credential, request.PathValue("taskID"), input.Attempt)
	if err != nil {
		if errors.Is(err, errStaleTaskLease) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"leaseExpiresAt": expiresAt.Format(time.RFC3339Nano)})
}

func agentCredential(request *http.Request) (string, error) {
	credential := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if credential == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		return "", errors.New("center: agent authentication required")
	}
	return credential, nil
}
