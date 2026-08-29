package center

import "net/http"

func (s *Server) handleCenterRemoteAccess(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		view, err := s.store.CenterRemoteAccess(request.Context(), s.infrastructure != nil)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, view)
		return
	}
	var input CenterRemoteAccessInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	network, err := s.store.CenterNetworkConfig(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.ConfigureCenterRemoteAccess(request.Context(), input, network.AgentConnectURL)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}
