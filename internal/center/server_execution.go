package center

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/petauron/vastora/internal/controlplane"
)

func (s *Server) handleGetExecutionClaimControl(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.ExecutionClaimControl(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleSetExecutionClaimControl(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		Paused *bool `json:"paused"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Paused == nil {
		writeError(w, http.StatusBadRequest, errors.New("center: paused is required"))
		return
	}
	if err := s.store.SetExecutionClaimControl(r.Context(), adminID, *input.Paused); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) handleImportLegacyReceipt(w http.ResponseWriter, r *http.Request) {
	credential, err := agentCredential(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	// Authenticate before allocating the larger, still bounded migration body.
	if err := s.store.authenticateAgent(r.Context(), r.PathValue("id"), credential); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.LegacyReceiptImport
	if err := decodeJSONLimit(r, &input, controlplane.LegacyReceiptMaxPayloadBytes); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.store.ImportLegacyReceipt(r.Context(), r.PathValue("id"), credential, input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": true, "id": id, "digest": input.Digest})
}

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	var before int64
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("center: invalid execution query"))
		return
	}
	for key, values := range query {
		if key != "before" || len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest, errors.New("center: invalid execution query"))
			return
		}
		before, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || before <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("center: invalid execution cursor"))
			return
		}
	}
	values, err := s.store.ListExecutions(r.Context(), before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, values)
}

func (s *Server) handleInspectLegacyReceipt(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.InspectLegacyReceipt(r.Context(), r.PathValue("executionID"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleReexecuteExecution(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionDisposition
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.ReexecuteExecution(r.Context(), r.PathValue("executionID"), adminID, input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) handleDisposeHelperExecution(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionDisposition
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DisposeHelperExecution(r.Context(), r.PathValue("executionID"), adminID, input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) handleConfirmExecution(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionDisposition
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.ConfirmExecution(r.Context(), r.PathValue("executionID"), adminID, input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) handleDisposeLegacyReceipt(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionDisposition
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.AbandonLegacyReceipt(r.Context(), r.PathValue("executionID"), adminID, input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) handleAbandonExecution(w http.ResponseWriter, r *http.Request) {
	adminID, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionDisposition
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.AbandonExecution(r.Context(), r.PathValue("executionID"), adminID, input); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) handleExecutionSession(w http.ResponseWriter, r *http.Request) {
	credential, err := agentCredential(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionSessionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RegisterExecutionSession(r.Context(), r.PathValue("id"), credential, input.SessionID, input.Protocol); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"registered": true})
}

func (s *Server) handleExecutionTransition(w http.ResponseWriter, r *http.Request) {
	credential, err := agentCredential(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	agentID := r.PathValue("id")
	if err := s.store.authenticateAgent(r.Context(), agentID, credential); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	var input controlplane.ExecutionTransitionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("executionID")
	if input.Action == "helper-observe" {
		ready, err := s.store.UpdateHelperObserved(r.Context(), agentID, input.SessionID, id)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"ready": ready})
		return
	}
	switch input.Action {
	case "start":
		err = s.store.StartExecution(r.Context(), agentID, input.SessionID, id, input.Digest)
	case "step":
		err = s.store.CheckExecutionStep(r.Context(), agentID, input.SessionID, id, input.Phase)
	case "renew":
		err = s.store.RenewExecution(r.Context(), agentID, input.SessionID, id)
	case "helper-step":
		err = s.store.CheckUpdateHelperStep(r.Context(), agentID, input.SessionID, id, input.Phase)
	case "stop":
		err = s.store.StopExecution(r.Context(), agentID, input.SessionID, id, input.Unknown, input.Error)
	default:
		writeError(w, http.StatusBadRequest, errors.New("center: invalid execution action"))
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}
