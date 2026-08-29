package center

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		SiteID       string `json:"siteId"`
		Name         string `json:"name"`
		CenterURL    string `json:"centerUrl"`
		UseHeadscale bool   `json:"useHeadscale"`
		Gateway      bool   `json:"gateway"`
		Tunnel       bool   `json:"tunnel"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	enrollment, err := s.store.CreateAgentEnrollment(request.Context(), AgentEnrollmentSpec{
		SiteID: input.SiteID, Name: input.Name, CenterURL: input.CenterURL,
		UseHeadscale: input.UseHeadscale, Gateway: input.Gateway, Tunnel: input.Tunnel,
	})
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
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

func (s *Server) handleDisableAgent(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DisableAgent(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"disabled": true})
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
		Version         string `json:"version"`
		OperatingSystem string `json:"operatingSystem"`
		Architecture    string `json:"architecture"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.EnrollAgent(request.Context(), input.Token, input.Version, input.OperatingSystem, input.Architecture)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusCreated, credential)
}

func (s *Server) handleAgentHeartbeat(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Version                      string                           `json:"version"`
		AppliedInstallations         int                              `json:"appliedInstallations"`
		Roles                        []string                         `json:"roles"`
		Capabilities                 NodeCapabilities                 `json:"capabilities"`
		NetworkCandidates            []networking.Candidate           `json:"networkCandidates"`
		ApplicationEndpoints         []ApplicationEndpointObservation `json:"applicationEndpoints"`
		ApplicationEndpointsObserved bool                             `json:"applicationEndpointsObserved"`
		GatewayHealthy               bool                             `json:"gatewayHealthy"`
		ApplicationRuntimeGeneration int                              `json:"applicationRuntimeGeneration"`
		TailscaleEnrolled            bool                             `json:"tailscaleEnrolled"`
		TailscaleOwnership           string                           `json:"tailscaleOwnership"`
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
	if err := s.store.RecordAgentHeartbeat(request.Context(), request.PathValue("id"), credential, NodeHeartbeat{Version: input.Version, AppliedInstallations: input.AppliedInstallations, Roles: input.Roles, Capabilities: input.Capabilities, NetworkCandidates: input.NetworkCandidates, ApplicationEndpoints: input.ApplicationEndpoints, ApplicationEndpointsObserved: input.ApplicationEndpointsObserved, GatewayHealthy: input.GatewayHealthy, ApplicationRuntimeGeneration: input.ApplicationRuntimeGeneration, TailscaleOwnership: input.TailscaleOwnership}); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
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
	writeJSON(writer, http.StatusOK, map[string]any{"connected": true, "centerUrl": centerURL, "tailscaleIsolation": isolation})
}

func (s *Server) handleClaimTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
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
	task, err := s.store.WaitAndClaimNextTask(request.Context(), request.PathValue("id"), credential, wait)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleCompleteTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		Attempt                int64           `json:"attempt"`
		Succeeded              bool            `json:"succeeded"`
		Error                  string          `json:"error"`
		Result                 json.RawMessage `json:"result"`
		ReconciliationRequired bool            `json:"reconciliationRequired"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.completeTaskWithDisposition(request.Context(), request.PathValue("id"), credential, request.PathValue("taskID"), input.Attempt, input.Succeeded, input.Error, input.Result, input.ReconciliationRequired); err != nil {
		if errors.Is(err, errInvalidReconciliationDisposition) {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"completed": true})
}

func agentCredential(request *http.Request) (string, error) {
	credential := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if credential == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		return "", errors.New("center: agent authentication required")
	}
	return credential, nil
}
