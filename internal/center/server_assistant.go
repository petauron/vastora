package center

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) requestAdminID(request *http.Request) (string, error) {
	cookie, err := request.Cookie("vastora_session")
	if err != nil {
		return "", err
	}
	return s.store.SessionAdminID(request.Context(), cookie.Value)
}

func (s *Server) handleAssistantProvider(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.AssistantProvider(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleSaveAssistantProvider(writer http.ResponseWriter, request *http.Request) {
	var input AssistantProviderInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.SaveAssistantProvider(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleValidateAssistantProvider(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.ValidateAssistantProvider(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleListAssistantConversations(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	values, err := s.store.ListAssistantConversations(request.Context(), adminID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"conversations": values})
}

func (s *Server) handleCreateAssistantConversation(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	value, err := s.store.CreateAssistantConversation(request.Context(), adminID, input.Title)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (s *Server) handleAssistantConversation(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	value, err := s.store.AssistantConversation(request.Context(), adminID, request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleCreateAssistantMessage(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	run, err := s.store.QueueAssistantMessage(request.Context(), adminID, request.PathValue("id"), input.Content)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.startAssistantRun(run, adminID)
	writeJSON(writer, http.StatusAccepted, run)
}

func (s *Server) handleCancelAssistantRun(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err := s.cancelAssistantRun(request.Context(), adminID, request.PathValue("id")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"cancelled": true})
}

func (s *Server) handleApproveAssistantProposal(writer http.ResponseWriter, request *http.Request) {
	s.handleAssistantProposalDecision(writer, request, "approved")
}

func (s *Server) handleRejectAssistantProposal(writer http.ResponseWriter, request *http.Request) {
	s.handleAssistantProposalDecision(writer, request, "rejected")
}

func (s *Server) handleAssistantProposalDecision(writer http.ResponseWriter, request *http.Request, decision string) {
	var input struct {
		Digest string `json:"digest"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	value, err := s.store.DecideAssistantProposal(request.Context(), adminID, request.PathValue("id"), decision, strings.TrimSpace(input.Digest))
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleApplyAssistantProposal(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Digest string `json:"digest"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	proposal, _, owner, err := s.store.assistantProposalByID(request.Context(), request.PathValue("id"))
	if err != nil || owner != adminID {
		writeError(writer, http.StatusNotFound, fmt.Errorf("center: assistant proposal not found"))
		return
	}
	execution, err := s.store.ApplyAssistantProposal(request.Context(), adminID, proposal.ID, strings.TrimSpace(input.Digest))
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if execution.Kind == "rotate_cpa_credential" {
		s.watchAssistantCredentialRotation(proposal, execution, adminID)
	} else {
		s.watchAssistantDeployment(proposal, execution, adminID)
	}
	writeJSON(writer, http.StatusAccepted, execution)
}

func (s *Server) handleAssistantEvents(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(request.Header.Get("Last-Event-ID")); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
	} else if raw = strings.TrimSpace(request.URL.Query().Get("after")); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
	}
	if err != nil || after < 0 {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("center: invalid assistant event cursor"))
		return
	}
	if _, err := s.store.AssistantEvents(request.Context(), adminID, request.PathValue("id"), after); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("center: assistant event streaming is unavailable"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(500 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer heartbeat.Stop()
	for {
		events, err := s.store.AssistantEvents(request.Context(), adminID, request.PathValue("id"), after)
		if err != nil {
			return
		}
		for _, event := range events {
			_, _ = fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Event, event.Data)
			after = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = writer.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		case <-ticker.C:
		}
	}
}
