package center

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
)

func (s *Server) handleListIntegrations(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListIntegrations(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"integrations": values})
}

func (s *Server) handleStartCloudflareOAuth(writer http.ResponseWriter, _ *http.Request) {
	value, err := s.store.StartCloudflareOAuth()
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}

func (s *Server) handlePollCloudflareOAuth(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.PollCloudflareOAuth(request.Context(), input.SessionID)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleCompleteCloudflareOAuth(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		SessionID string `json:"sessionId"`
		ZoneID    string `json:"zoneId"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.CompleteCloudflareOAuth(request.Context(), input.SessionID, input.ZoneID)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleConfigureSetupDNS(writer http.ResponseWriter, request *http.Request) {
	var input SetupDNSInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	candidates, err := networking.Discover(s.store.now().UTC())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	value, err := s.store.ConfigureSetupDNS(request.Context(), input, candidates)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"records": value})
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
