package center

import "net/http"

func (s *Server) handlePreviewServiceHandover(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.PreviewServiceHandover(r.Context(), r.PathValue("id"), r.URL.Query().Get("targetServiceId"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleServiceHandover(w http.ResponseWriter, r *http.Request) {
	var input ServiceHandover
	if err := decodeJSONLimit(r, &input, 4096); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.store.HandoverService(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
