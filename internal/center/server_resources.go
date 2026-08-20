package center

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleListDeployments(writer http.ResponseWriter, request *http.Request) {
	deployments, err := s.store.ListDeployments(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"deployments": deployments})
}

func (s *Server) handleCreateDeployment(writer http.ResponseWriter, request *http.Request) {
	var input DeploymentRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	deployment, err := s.store.CreateDeployment(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, deployment)
}

func (s *Server) handleListSites(writer http.ResponseWriter, request *http.Request) {
	sites, err := s.store.ListSites(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sites": sites})
}

func (s *Server) handleListOrganizations(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListOrganizations(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"organizations": values})
}

func (s *Server) handleCreateSite(writer http.ResponseWriter, request *http.Request) {
	var input SiteInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	site, err := s.store.CreateSite(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, site)
}

func (s *Server) handleUpdateSite(writer http.ResponseWriter, request *http.Request) {
	var input SiteInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	site, err := s.store.UpdateSite(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, site)
}

func (s *Server) handleListApplications(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListApplications(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"applications": values})
}

func (s *Server) handleListServices(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListServices(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"services": values})
}

func (s *Server) handleListPublications(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListPublications(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"publications": values})
}

func (s *Server) handleCreatePublication(writer http.ResponseWriter, request *http.Request) {
	var input PublicationInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.CreatePublication(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (s *Server) handleStopPublication(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.StopPublication(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"stopped": true})
}

func (s *Server) handleVerifyPublication(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.VerifyPublication(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleListRoutes(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListRoutes(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"routes": values})
}

func (s *Server) handleListActions(writer http.ResponseWriter, request *http.Request) {
	limit := defaultActionLimit
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(writer, http.StatusBadRequest, errors.New("center: action limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	values, err := s.store.ListActions(request.Context(), limit)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"actions": values})
}
