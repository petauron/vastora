package center

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

var Version = "0.1.0-dev"

type Server struct {
	store                *Store
	staticDir            string
	agentBinariesDir     string
	setupAgentConnectURL string
	secureCookies        bool
	officialCatalog      []byte
	headscaleInstaller   deployapi.HeadscaleInstaller
}

func (s *Server) WithHeadscaleInstaller(installer deployapi.HeadscaleInstaller) *Server {
	s.headscaleInstaller = installer
	return s
}

func NewServer(store *Store, staticDir string, secureCookies bool) *Server {
	return &Server{store: store, staticDir: staticDir, secureCookies: secureCookies}
}

func (s *Server) WithOfficialCatalog(payload []byte) *Server {
	s.officialCatalog = payload
	return s
}

func (s *Server) WithAgentBinaries(path string) *Server {
	s.agentBinariesDir = path
	return s
}

func (s *Server) WithSetupAgentConnectURL(value string) *Server {
	s.setupAgentConnectURL = value
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /install/agent.sh", s.handleAgentInstallScript)
	mux.HandleFunc("GET /api/v1/agent-binaries/{os}/{arch}", s.handleAgentBinary)
	mux.HandleFunc("GET /api/v1/agents/{id}/binary/{os}/{arch}", s.handleAgentUpdateBinary)
	mux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup/admin", s.handleSetupAdmin)
	mux.HandleFunc("POST /api/v1/setup/complete", s.requireAuth(true, s.handleSetupComplete))
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(true, s.handleLogout))
	mux.HandleFunc("PUT /api/v1/auth/password", s.requireAuth(true, s.handleChangePassword))
	mux.HandleFunc("GET /api/v1/status", s.requireAuth(false, s.handleStatus))
	mux.HandleFunc("GET /api/v1/diagnostics", s.requireAuth(false, s.handleDiagnostics))
	mux.HandleFunc("POST /api/v1/backups", s.requireAuth(true, s.handleCreateBackup))
	mux.HandleFunc("GET /api/v1/deployments", s.requireAuth(false, s.handleListDeployments))
	mux.HandleFunc("POST /api/v1/deployments", s.requireAuth(true, s.handleCreateDeployment))
	mux.HandleFunc("GET /api/v1/organizations", s.requireAuth(false, s.handleListOrganizations))
	mux.HandleFunc("GET /api/v1/sites", s.requireAuth(false, s.handleListSites))
	mux.HandleFunc("POST /api/v1/sites", s.requireAuth(true, s.handleCreateSite))
	mux.HandleFunc("PUT /api/v1/sites/{id}", s.requireAuth(true, s.handleUpdateSite))
	mux.HandleFunc("GET /api/v1/applications", s.requireAuth(false, s.handleListApplications))
	mux.HandleFunc("GET /api/v1/applications/{id}/commands/latest", s.requireAuth(false, s.handleLatestApplicationCommand))
	mux.HandleFunc("POST /api/v1/application-commands/reality", s.requireAuth(true, s.handleCreateRealityCommand))
	mux.HandleFunc("POST /api/v1/application-commands/subscription", s.requireAuth(true, s.handleCreateSubscriptionCommand))
	mux.HandleFunc("POST /api/v1/application-commands/clients", s.requireAuth(true, s.handleCreateThreeXUIClientCommand))
	mux.HandleFunc("GET /api/v1/application-commands/{id}", s.requireAuth(false, s.handleApplicationCommand))
	mux.HandleFunc("POST /api/v1/application-commands/{id}/reveal", s.requireAuth(true, s.handleRevealApplicationCommand))
	mux.HandleFunc("GET /api/v1/services", s.requireAuth(false, s.handleListServices))
	mux.HandleFunc("GET /api/v1/publications", s.requireAuth(false, s.handleListPublications))
	mux.HandleFunc("POST /api/v1/publications", s.requireAuth(true, s.handleCreatePublication))
	mux.HandleFunc("DELETE /api/v1/publications/{id}", s.requireAuth(true, s.handleStopPublication))
	mux.HandleFunc("POST /api/v1/publications/{id}/verify", s.requireAuth(true, s.handleVerifyPublication))
	mux.HandleFunc("GET /api/v1/routes", s.requireAuth(false, s.handleListRoutes))
	mux.HandleFunc("GET /api/v1/integrations", s.requireAuth(false, s.handleListIntegrations))
	mux.HandleFunc("POST /api/v1/integrations/cloudflare/oauth/start", s.requireAuth(true, s.handleStartCloudflareOAuth))
	mux.HandleFunc("POST /api/v1/integrations/cloudflare/oauth/poll", s.requireAuth(true, s.handlePollCloudflareOAuth))
	mux.HandleFunc("POST /api/v1/integrations/cloudflare/oauth/complete", s.requireAuth(true, s.handleCompleteCloudflareOAuth))
	mux.HandleFunc("POST /api/v1/setup/cloudflare/dns", s.requireAuth(true, s.handleConfigureSetupDNS))
	mux.HandleFunc("PUT /api/v1/integrations/headscale", s.requireAuth(true, s.handleConfigureHeadscale))
	mux.HandleFunc("POST /api/v1/agents/{id}/headscale-join", s.requireAuth(true, s.handleCreateHeadscaleJoin))
	mux.HandleFunc("GET /api/v1/actions", s.requireAuth(false, s.handleListActions))
	mux.HandleFunc("GET /api/v1/agents", s.requireAuth(false, s.handleListAgents))
	mux.HandleFunc("POST /api/v1/agent-enrollments", s.requireAuth(true, s.handleCreateAgentEnrollment))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.requireAuth(true, s.handleUpdateAgent))
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.requireAuth(true, s.handleDisableAgent))
	mux.HandleFunc("PUT /api/v1/agents/{id}/network-profile", s.requireAuth(true, s.handleConfirmNetworkProfile))
	mux.HandleFunc("POST /api/v1/agents/enroll", s.handleEnrollAgent)
	mux.HandleFunc("POST /api/v1/agents/{id}/heartbeat", s.handleAgentHeartbeat)
	mux.HandleFunc("GET /api/v1/agents/{id}/tasks/next", s.handleClaimTask)
	mux.HandleFunc("POST /api/v1/agents/{id}/tasks/{taskID}/result", s.handleCompleteTask)
	mux.HandleFunc("GET /api/v1/catalog/official", s.handleOfficialCatalog)
	mux.HandleFunc("GET /api/v1/catalog/sources", s.requireAuth(false, s.handleListSources))
	mux.HandleFunc("POST /api/v1/catalog/sources", s.requireAuth(true, s.handleCreateSource))
	mux.HandleFunc("POST /api/v1/catalog/sources/{id}/refresh", s.requireAuth(true, s.handleRefreshSource))
	mux.HandleFunc("GET /api/v1/catalog/apps", s.requireAuth(false, s.handleListApps))
	mux.HandleFunc("POST /api/v1/registry-credentials", s.requireAuth(true, s.handleCreateRegistryCredential))
	mux.Handle("/api/", http.NotFoundHandler())
	mux.Handle("/", s.staticHandler())
	return securityHeaders(mux)
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (s *Server) requireAuth(mutation bool, handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("vastora_session")
		if err != nil {
			writeError(writer, http.StatusUnauthorized, errors.New("center: authentication required"))
			return
		}
		if err := s.store.ValidateSession(request.Context(), cookie.Value, request.Header.Get("X-CSRF-Token"), mutation); err != nil {
			writeError(writer, http.StatusUnauthorized, err)
			return
		}
		handler(writer, request)
	}
}

func (s *Server) setSessionCookies(writer http.ResponseWriter, session, csrf string) {
	expires := time.Now().Add(sessionLifetime)
	http.SetCookie(writer, &http.Cookie{Name: "vastora_session", Value: session, Path: "/", Expires: expires, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
	http.SetCookie(writer, &http.Cookie{Name: "vastora_csrf", Value: csrf, Path: "/", Expires: expires, HttpOnly: false, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
}

func (s *Server) clearSessionCookies(writer http.ResponseWriter) {
	for _, name := range []string{"vastora_session", "vastora_csrf"} {
		http.SetCookie(writer, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == "vastora_session", SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
	}
}

func (s *Server) staticHandler() http.Handler {
	if s.staticDir == "" {
		return http.NotFoundHandler()
	}
	index := filepath.Join(s.staticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(spaFileSystem{root: http.Dir(s.staticDir)})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(writer, request)
			return
		}
		files.ServeHTTP(writer, request)
	})
}

type spaFileSystem struct {
	root http.FileSystem
}

func (files spaFileSystem) Open(name string) (http.File, error) {
	file, err := files.root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return files.root.Open("/index.html")
	}
	return file, err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'")
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(request *http.Request, target any) error {
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		return errors.New("center: Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("center: decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("center: request must contain one JSON value")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	message := "request failed"
	if err != nil {
		message = err.Error()
	}
	writeJSON(writer, status, map[string]string{"code": errorCode(status, message), "error": message})
}

func errorCode(status int, message string) string {
	normalized := strings.ToLower(message)
	switch {
	case strings.Contains(normalized, "authentication required"):
		return "authentication_required"
	case strings.Contains(normalized, "already installed"):
		return "already_installed"
	case strings.Contains(normalized, "dns record") && strings.Contains(normalized, "already exists"):
		return "dns_record_conflict"
	case strings.Contains(normalized, "cloudflare"):
		return "cloudflare_error"
	case strings.Contains(normalized, "gateway") && (strings.Contains(normalized, "unavailable") || strings.Contains(normalized, "required")):
		return "gateway_unavailable"
	}
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "authentication_required"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	default:
		return "internal_error"
	}
}

// ContextWithTimeout is exposed for callers that refresh multiple sources in
// parallel without leaking request cancellation across jobs.
func ContextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 15*time.Second)
}
