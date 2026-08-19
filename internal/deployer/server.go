package deployer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/petauron/vastora/internal/deployapi"
)

type Server struct {
	installer deployapi.HeadscaleInstaller
}

func NewServer(installer deployapi.HeadscaleInstaller) *Server {
	return &Server{installer: installer}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/headscale/install", server.installHeadscale)
	return mux
}

func (server *Server) installHeadscale(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusBadRequest, errors.New("deployer: Content-Type must be application/json"))
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var input deployapi.HeadscaleInstallRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("deployer: decode request: %w", err))
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, errors.New("deployer: request must contain one JSON value"))
		return
	}
	result, err := server.installer.InstallHeadscale(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func ServeUnix(socket string, uid, gid int, handler http.Handler) error {
	if !filepath.IsAbs(socket) {
		return errors.New("deployer: Unix socket path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		return fmt.Errorf("deployer: create socket directory: %w", err)
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("deployer: socket path is occupied by a non-socket file")
		}
		if err := os.Remove(socket); err != nil {
			return fmt.Errorf("deployer: remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deployer: inspect socket path: %w", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("deployer: listen on Unix socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chown(socket, uid, gid); err != nil {
		return fmt.Errorf("deployer: set socket owner: %w", err)
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		return fmt.Errorf("deployer: set socket permissions: %w", err)
	}
	return http.Serve(listener, handler)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	message := "deployment failed"
	if err != nil {
		message = err.Error()
	}
	writeJSON(writer, status, map[string]string{"error": message})
}
