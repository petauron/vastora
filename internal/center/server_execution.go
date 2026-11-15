package center

import (
	"database/sql"
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

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	var before int64
	filter := "all"
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("center: invalid execution query"))
		return
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest, errors.New("center: invalid execution query"))
			return
		}
		switch key {
		case "filter":
			filter = values[0]
			if filter != "all" && filter != "attention" && filter != "running" {
				writeError(w, http.StatusBadRequest, errors.New("center: invalid execution filter"))
				return
			}
		case "before":
			before, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || before <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("center: invalid execution cursor"))
				return
			}
		default:
			writeError(w, http.StatusBadRequest, errors.New("center: invalid execution query"))
			return
		}
	}
	values, err := s.store.ListExecutions(r.Context(), before, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, values)
}

func (s *Server) handleTaskExecution(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.TaskExecution(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("center: task execution not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, value)
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
