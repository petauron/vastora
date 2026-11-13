package center

import "net/http"

func (s *Server) handleEnsureManagedDomain(w http.ResponseWriter, r *http.Request) {
	var input ManagedDomainInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.EnsureManagedDomain(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) handleListManagedDomains(w http.ResponseWriter, r *http.Request) {
	values, err := s.store.ListManagedDomains(r.Context(), r.URL.Query().Get("applicationId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": values})
}
func (s *Server) handleManagedDomain(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.ManagedDomain(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) handleRetireManagedDomain(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RetireManagedDomain(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"retired": true})
}
