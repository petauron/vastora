package center

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/petauron/vastora/internal/networking"
)

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
		LandingOnly      bool   `json:"landingOnly"`
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
		LandingOnly: input.LandingOnly,
		SiteID:      input.SiteID, Name: input.Name, CenterURL: input.CenterURL,
		UseHeadscale: input.UseHeadscale, Gateway: input.Gateway, Tunnel: input.Tunnel,
		CAFingerprint: input.CAFingerprint, CACertificatePEM: input.CACertificatePEM,
	})
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	statusID, err := s.store.agentEnrollmentStatusID(enrollment.Token)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("center: enrollment status reference could not be prepared"))
		return
	}
	writeJSON(writer, http.StatusCreated, AgentEnrollmentCommand{AgentEnrollment: enrollment, StatusID: statusID})
}

func (s *Server) handleCreateAgentReconnectEnrollment(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	enrollment, err := s.store.CreateAgentReconnectEnrollment(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, enrollment)
}

func (s *Server) handleContinueAgentReinstallIsolation(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallContinueInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	enrollment, err := s.store.ContinueAgentReinstallIsolation(request.Context(), request.PathValue("id"), adminID, input)
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
