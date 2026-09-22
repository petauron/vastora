package center

import "net/http"

func (s *Server) handleMeridianInventory(writer http.ResponseWriter, request *http.Request) {
	view, err := s.store.MeridianInventory(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) handleMeridianCutover(writer http.ResponseWriter, request *http.Request) {
	view, err := s.store.MeridianCutover(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) handleStartMeridianCutover(writer http.ResponseWriter, request *http.Request) {
	view, err := s.store.StartMeridianCutover(request.Context())
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, view)
}

func (s *Server) handleCreateMeridianEndpoint(writer http.ResponseWriter, request *http.Request) {
	var input MeridianEndpointInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.CreateMeridianEndpoint(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (s *Server) handleUpdateMeridianEndpointName(writer http.ResponseWriter, request *http.Request) {
	var input MeridianEndpointNameInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.UpdateMeridianEndpointName(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) handleRecoverMeridianEndpoint(writer http.ResponseWriter, request *http.Request) {
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input MeridianEndpointRecoveryInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RecoverMeridianEndpoint(request.Context(), request.PathValue("id"), adminID, input); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"queued": true})
}

func (s *Server) handleCreateMeridianAccount(writer http.ResponseWriter, request *http.Request) {
	var input MeridianAccountInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.CreateMeridianAccount(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, view)
}

func (s *Server) handleUpdateMeridianAccount(writer http.ResponseWriter, request *http.Request) {
	var input MeridianAccountInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.UpdateMeridianAccount(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) handleCreateMeridianRouteGrant(writer http.ResponseWriter, request *http.Request) {
	var input MeridianRouteGrantInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.CreateMeridianRouteGrant(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (s *Server) handleRevokeMeridianRouteGrant(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.RevokeMeridianRouteGrant(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"accepted": true})
}
