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
	"sync"

	"github.com/petauron/vastora/internal/deployapi"
)

type Server struct {
	installer         deployapi.HeadscaleInstaller
	publicEntryProber deployapi.PublicEntryProber
	centerUpdater     deployapi.CenterUpdater
	remoteAccess      deployapi.CenterRemoteAccessManager
	headscaleMu       sync.Mutex
}

func NewServer(installer deployapi.HeadscaleInstaller) *Server {
	return &Server{installer: installer, publicEntryProber: NewPublicEntryProbeService("", "")}
}

func (server *Server) WithPublicEntryProber(prober deployapi.PublicEntryProber) *Server {
	server.publicEntryProber = prober
	return server
}

func (server *Server) WithCenterUpdater(updater deployapi.CenterUpdater) *Server {
	server.centerUpdater = updater
	return server
}

func (server *Server) WithCenterRemoteAccessManager(manager deployapi.CenterRemoteAccessManager) *Server {
	server.remoteAccess = manager
	return server
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/headscale/install", server.installHeadscale)
	mux.HandleFunc("POST /v1/headscale/reconcile", server.reconcileHeadscale)
	mux.HandleFunc("POST /v1/public-entry/probes", server.startPublicEntryProbe)
	mux.HandleFunc("DELETE /v1/public-entry/probes/{id}", server.stopPublicEntryProbe)
	mux.HandleFunc("GET /v1/center/update", server.centerUpdateStatus)
	mux.HandleFunc("POST /v1/center/update", server.startCenterUpdate)
	mux.HandleFunc("PUT /v1/center/remote-access", server.applyCenterRemoteAccess)
	return mux
}

func (server *Server) applyCenterRemoteAccess(writer http.ResponseWriter, request *http.Request) {
	if server.remoteAccess == nil {
		writeError(writer, http.StatusConflict, errors.New("deployer: Center remote access is unavailable"))
		return
	}
	var input deployapi.CenterRemoteAccessRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	if err := server.remoteAccess.ApplyCenterRemoteAccess(request.Context(), input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (server *Server) centerUpdateStatus(writer http.ResponseWriter, request *http.Request) {
	if server.centerUpdater == nil {
		writeJSON(writer, http.StatusOK, deployapi.CenterUpdateExecution{State: "idle"})
		return
	}
	result, err := server.centerUpdater.CenterUpdateStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) startCenterUpdate(writer http.ResponseWriter, request *http.Request) {
	if server.centerUpdater == nil {
		writeError(writer, http.StatusConflict, errors.New("deployer: automatic Center updates are unavailable"))
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !decodeRequest(writer, request, &input) {
		return
	}
	result, err := server.centerUpdater.StartCenterUpdate(request.Context(), input.Version)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) startPublicEntryProbe(writer http.ResponseWriter, request *http.Request) {
	var input deployapi.PublicEntryProbeRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	result, err := server.publicEntryProber.StartPublicEntryProbe(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) stopPublicEntryProbe(writer http.ResponseWriter, request *http.Request) {
	if err := server.publicEntryProber.StopPublicEntryProbe(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "stopped"})
}

func (server *Server) reconcileHeadscale(writer http.ResponseWriter, request *http.Request) {
	input, ok := decodeHeadscaleRequest(writer, request)
	if !ok {
		return
	}
	server.headscaleMu.Lock()
	defer server.headscaleMu.Unlock()
	if err := server.installer.ReconcileHeadscale(request.Context(), input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (server *Server) installHeadscale(writer http.ResponseWriter, request *http.Request) {
	input, ok := decodeHeadscaleRequest(writer, request)
	if !ok {
		return
	}
	server.headscaleMu.Lock()
	defer server.headscaleMu.Unlock()
	result, err := server.installer.InstallHeadscale(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func decodeHeadscaleRequest(writer http.ResponseWriter, request *http.Request) (deployapi.HeadscaleInstallRequest, bool) {
	var input deployapi.HeadscaleInstallRequest
	ok := decodeRequest(writer, request, &input)
	return input, ok
}

func decodeRequest(writer http.ResponseWriter, request *http.Request, input any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusBadRequest, errors.New("deployer: Content-Type must be application/json"))
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("deployer: decode request: %w", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, errors.New("deployer: request must contain one JSON value"))
		return false
	}
	return true
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
