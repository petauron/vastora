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
func (s *Server) handleHostBackup(writer http.ResponseWriter, request *http.Request) {
	if s.hostBackups == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("host backup helper is unavailable"))
		return
	}
	var result deployapi.HostBackupStatus
	var err error
	switch request.Method {
	case http.MethodPut:
		var input deployapi.HostBackupInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		result, err = s.hostBackups.ConfigureHostBackup(request.Context(), input)
	case http.MethodPost:
		result, err = s.hostBackups.StartHostBackup(request.Context())
	default:
		result, err = s.hostBackups.HostBackupStatus(request.Context())
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
