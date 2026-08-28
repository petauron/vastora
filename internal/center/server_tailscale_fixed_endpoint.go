package center

import "net/http"

func (s *Server) handleTailscaleFixedEndpoint(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		view, err := s.store.TailscaleFixedEndpoint(request.Context())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, view)
		return
	}
	var input TailscaleFixedEndpointInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.ConfigureTailscaleFixedEndpoint(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}
