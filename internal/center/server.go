package center

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
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
	mux.HandleFunc("GET /api/v1/dashboard", s.requireAuth(false, s.handleDashboard))
	mux.HandleFunc("GET /api/v1/diagnostics", s.requireAuth(false, s.handleDiagnostics))
	mux.HandleFunc("POST /api/v1/backups", s.requireAuth(true, s.handleCreateBackup))
	mux.HandleFunc("GET /api/v1/deployments", s.requireAuth(false, s.handleListDeployments))
	mux.HandleFunc("POST /api/v1/deployments", s.requireAuth(true, s.handleCreateDeployment))
	mux.HandleFunc("GET /api/v1/organizations", s.requireAuth(false, s.handleListOrganizations))
	mux.HandleFunc("GET /api/v1/sites", s.requireAuth(false, s.handleListSites))
	mux.HandleFunc("POST /api/v1/sites", s.requireAuth(true, s.handleCreateSite))
	mux.HandleFunc("PUT /api/v1/sites/{id}", s.requireAuth(true, s.handleUpdateSite))
	mux.HandleFunc("GET /api/v1/applications", s.requireAuth(false, s.handleListApplications))
	mux.HandleFunc("GET /api/v1/services", s.requireAuth(false, s.handleListServices))
	mux.HandleFunc("GET /api/v1/publications", s.requireAuth(false, s.handleListPublications))
	mux.HandleFunc("POST /api/v1/publications", s.requireAuth(true, s.handleCreatePublication))
	mux.HandleFunc("DELETE /api/v1/publications/{id}", s.requireAuth(true, s.handleStopPublication))
	mux.HandleFunc("POST /api/v1/publications/{id}/verify", s.requireAuth(true, s.handleVerifyPublication))
	mux.HandleFunc("GET /api/v1/routes", s.requireAuth(false, s.handleListRoutes))
	mux.HandleFunc("GET /api/v1/integrations", s.requireAuth(false, s.handleListIntegrations))
	mux.HandleFunc("PUT /api/v1/integrations/cloudflare", s.requireAuth(true, s.handleConfigureCloudflare))
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
	mux.Handle("/", s.staticHandler())
	return securityHeaders(mux)
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (s *Server) handleSetupStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := s.store.SetupStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"administratorConfigured":   status.AdministratorConfigured,
		"onboardingComplete":        status.OnboardingComplete,
		"suggestedAgentConnectUrl":  s.setupAgentConnectURL,
		"builtinHeadscaleAvailable": s.headscaleInstaller != nil,
	})
}

func (s *Server) handleSetupAdmin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	session, csrf, err := s.store.CreateFirstAdmin(request.Context(), input.Username, input.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.setSessionCookies(writer, session, csrf)
	writeJSON(writer, http.StatusCreated, map[string]bool{"administratorConfigured": true})
}

func (s *Server) handleSetupComplete(writer http.ResponseWriter, request *http.Request) {
	var input InitialSetupInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Network.AgentConnectionMode == "headscale" && input.Headscale != nil {
		if _, err := s.configureHeadscale(request.Context(), *input.Headscale, input.Network.AgentConnectURL); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
	}
	result, err := s.store.CompleteInitialSetup(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	session, csrf, err := s.store.Authenticate(request.Context(), input.Username, input.Password)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.setSessionCookies(writer, session, csrf)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie("vastora_session")
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("center: authentication required"))
		return
	}
	if err := s.store.Logout(request.Context(), cookie.Value); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.clearSessionCookies(writer)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleChangePassword(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	cookie, err := request.Cookie("vastora_session")
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("center: authentication required"))
		return
	}
	if err := s.store.ChangePassword(request.Context(), cookie.Value, input.CurrentPassword, input.NewPassword); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"changed": true})
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	networkConfig, err := s.store.CenterNetworkConfig(request.Context())
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	sources, err := s.store.ListSources(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	apps, err := s.store.ListApps(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	agents, err := s.store.ListAgents(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	deployments, err := s.store.ListDeployments(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"version":                 Version,
		"catalogSources":          len(sources),
		"catalogApps":             len(apps),
		"agents":                  countActiveAgents(agents),
		"deployments":             len(deployments),
		"agentInstallerAvailable": s.agentInstallerAvailable(),
		"agentConnectionMode":     networkConfig.AgentConnectionMode,
		"agentConnectUrl":         networkConfig.AgentConnectURL,
	})
}

func (s *Server) handleListDeployments(writer http.ResponseWriter, request *http.Request) {
	deployments, err := s.store.ListDeployments(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"deployments": deployments})
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

func (s *Server) handleListIntegrations(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListIntegrations(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"integrations": values})
}

func (s *Server) handleConfigureCloudflare(writer http.ResponseWriter, request *http.Request) {
	var input CloudflareInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.ConfigureCloudflare(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleConfigureHeadscale(writer http.ResponseWriter, request *http.Request) {
	var input HeadscaleInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	network, err := s.store.CenterNetworkConfig(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.configureHeadscale(request.Context(), input, network.AgentConnectURL)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) configureHeadscale(ctx context.Context, input HeadscaleInput, centerURL string) (IntegrationView, error) {
	if strings.TrimSpace(input.Mode) != "builtin" {
		return s.store.ConfigureHeadscale(ctx, input)
	}
	if s.headscaleInstaller == nil {
		return IntegrationView{}, errors.New("center: this installation does not include the Headscale deployment helper")
	}
	if strings.TrimSpace(input.APIKey) != "" {
		return IntegrationView{}, errors.New("center: built-in Headscale creates its API key automatically")
	}
	result, err := s.headscaleInstaller.InstallHeadscale(ctx, deployapi.HeadscaleInstallRequest{
		CenterURL:    centerURL,
		HeadscaleURL: input.URL,
	})
	if err != nil {
		return IntegrationView{}, err
	}
	return s.store.ConfigureBuiltinHeadscale(ctx, result.Endpoint, result.APIKey)
}

func (s *Server) handleCreateHeadscaleJoin(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.CreateHeadscaleJoin(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (s *Server) handleListActions(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListActions(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"actions": values})
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

func (s *Server) handleListAgents(writer http.ResponseWriter, request *http.Request) {
	agents, err := s.store.ListAgents(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleCreateAgentEnrollment(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SiteID       string `json:"siteId"`
		UseHeadscale bool   `json:"useHeadscale"`
		Gateway      bool   `json:"gateway"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	enrollment, err := s.store.CreateAgentEnrollment(request.Context(), input.SiteID)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.UseHeadscale {
		join, err := s.store.CreateHeadscaleBootstrap(request.Context(), input.Gateway)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		enrollment.HeadscaleCommand = join.Command
		enrollment.HeadscaleExpiresAt = join.ExpiresAt
	}
	writeJSON(writer, http.StatusCreated, enrollment)
}

func (s *Server) handleUpdateAgent(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SiteID string `json:"siteId"`
		Name   string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdateAgent(request.Context(), request.PathValue("id"), input.Name, input.SiteID); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"updated": true})
}

func (s *Server) handleDisableAgent(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DisableAgent(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"disabled": true})
}

func (s *Server) handleConfirmNetworkProfile(writer http.ResponseWriter, request *http.Request) {
	var input networking.Profile
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	profile, err := s.store.ConfirmNetworkProfile(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

func (s *Server) handleEnrollAgent(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Token   string `json:"token"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.EnrollAgent(request.Context(), input.Token, input.Name, input.Version)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusCreated, credential)
}

func (s *Server) handleAgentHeartbeat(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Version                      string                           `json:"version"`
		AppliedInstallations         int                              `json:"appliedInstallations"`
		Roles                        []string                         `json:"roles"`
		Capabilities                 NodeCapabilities                 `json:"capabilities"`
		NetworkCandidates            []networking.Candidate           `json:"networkCandidates"`
		ApplicationEndpoints         []ApplicationEndpointObservation `json:"applicationEndpoints"`
		ApplicationEndpointsObserved bool                             `json:"applicationEndpointsObserved"`
		GatewayHealthy               bool                             `json:"gatewayHealthy"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if credential == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		writeError(writer, http.StatusUnauthorized, errors.New("center: agent authentication required"))
		return
	}
	if err := s.store.RecordAgentHeartbeat(request.Context(), request.PathValue("id"), credential, NodeHeartbeat{Version: input.Version, AppliedInstallations: input.AppliedInstallations, Roles: input.Roles, Capabilities: input.Capabilities, NetworkCandidates: input.NetworkCandidates, ApplicationEndpoints: input.ApplicationEndpoints, ApplicationEndpointsObserved: input.ApplicationEndpointsObserved, GatewayHealthy: input.GatewayHealthy}); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"connected": true})
}

func (s *Server) handleClaimTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	task, err := s.store.ClaimNextTask(request.Context(), request.PathValue("id"), credential)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleCompleteTask(writer http.ResponseWriter, request *http.Request) {
	credential, err := agentCredential(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var input struct {
		Attempt   int64           `json:"attempt"`
		Succeeded bool            `json:"succeeded"`
		Error     string          `json:"error"`
		Result    json.RawMessage `json:"result"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.CompleteTask(request.Context(), request.PathValue("id"), credential, request.PathValue("taskID"), input.Attempt, input.Succeeded, input.Error, input.Result); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"completed": true})
}

func (s *Server) handleOfficialCatalog(writer http.ResponseWriter, request *http.Request) {
	envelope, err := s.store.OfficialCatalogEnvelope(request.Context())
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(envelope)
}

func agentCredential(request *http.Request) (string, error) {
	credential := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if credential == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		return "", errors.New("center: agent authentication required")
	}
	return credential, nil
}

func (s *Server) handleListSources(writer http.ResponseWriter, request *http.Request) {
	sources, err := s.store.ListSources(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sources": sources})
}

func (s *Server) handleCreateSource(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ID             string `json:"id"`
		DisplayName    string `json:"displayName"`
		URL            string `json:"url"`
		PublicKey      string `json:"publicKey"`
		BearerToken    string `json:"bearerToken"`
		CustomCAPEM    string `json:"customCA"`
		RefreshSeconds int    `json:"refreshIntervalSeconds"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(input.PublicKey)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("center: public key must be base64url"))
		return
	}
	if err := s.store.CreateSource(request.Context(), SourceInput{
		ID: input.ID, DisplayName: input.DisplayName, URL: input.URL, PublicKey: publicKey,
		BearerToken: input.BearerToken, CustomCAPEM: input.CustomCAPEM, RefreshSeconds: input.RefreshSeconds,
	}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"id": input.ID})
}

func (s *Server) handleRefreshSource(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("id")
	if identifier == OfficialCatalogSourceID {
		if len(s.officialCatalog) == 0 {
			writeError(writer, http.StatusNotFound, errors.New("center: official catalog is unavailable"))
			return
		}
		if err := s.store.SeedOfficialCatalog(request.Context(), s.officialCatalog); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "apps": 0, "notModified": false})
		return
	}
	source, err := s.store.SourceForRefresh(request.Context(), identifier)
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	result, err := catalog.Fetch(request.Context(), catalog.FetchConfig{
		URL: source.URL, PublicKey: ed25519.PublicKey(source.publicKey), BearerToken: source.bearerToken,
		CustomCAPEM: source.customCA, ETag: source.etag, LastModified: source.lastMod,
	})
	if err != nil {
		_ = s.store.RecordCatalogError(request.Context(), identifier, err)
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	if result.NotModified {
		if err := s.store.ClearCatalogError(request.Context(), identifier); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "notModified": true})
		return
	}
	if err := s.store.SaveCatalog(request.Context(), identifier, result.RawEnvelope, result.ETag, result.LastModified); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "apps": len(result.Catalog.Apps), "notModified": false})
}

func (s *Server) handleListApps(writer http.ResponseWriter, request *http.Request) {
	apps, err := s.store.ListApps(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"apps": apps})
}

func (s *Server) handleCreateRegistryCredential(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.CreateRegistryCredential(request.Context(), input.Host, input.Username, input.Token)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, credential)
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
	writeJSON(writer, status, map[string]string{"error": message})
}

// ContextWithTimeout is exposed for callers that refresh multiple sources in
// parallel without leaking request cancellation across jobs.
func ContextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 15*time.Second)
}
