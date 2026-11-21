package center

import (
	"errors"
	"net/http"

	"github.com/petauron/vastora/internal/deployapi"
)

func (s *Server) WithHostBackupManager(manager deployapi.HostBackupManager) *Server {
	s.hostBackups = manager
	return s
}

func (s *Server) handleHostBackupStatus(writer http.ResponseWriter, request *http.Request) {
	if s.hostBackups == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("host backup helper is unavailable"))
		return
	}
	result, err := s.hostBackups.HostBackupStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleConfigureHostBackup(writer http.ResponseWriter, request *http.Request) {
	if s.hostBackups == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("host backup helper is unavailable"))
		return
	}
	var input deployapi.HostBackupInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := s.hostBackups.ConfigureHostBackup(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleStartHostBackup(writer http.ResponseWriter, request *http.Request) {
	if s.hostBackups == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("host backup helper is unavailable"))
		return
	}
	result, err := s.hostBackups.StartHostBackup(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
