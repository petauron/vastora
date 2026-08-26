package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/petauron/vastora/internal/deployapi"
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

func (s *Server) handleListCloudflareZones(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.CloudflareZones(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"zones": values})
}

func (s *Server) handleSystemDomain(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.SystemDomain(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleSwitchSystemDomain(writer http.ResponseWriter, request *http.Request) {
	var input SystemDomainSwitchInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.SwitchSystemDomain(request.Context(), input)
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
	candidates, err := s.store.discoverNetworkCandidates(s.store.now().UTC())
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
	if s.infrastructure == nil {
		return IntegrationView{}, errors.New("center: this installation does not include the Headscale deployment helper")
	}
	if strings.TrimSpace(input.APIKey) != "" {
		return IntegrationView{}, errors.New("center: built-in Headscale creates its API key automatically")
	}
	centerCertificate, _, err := s.store.ensureSystemCenterCertificate(ctx, centerURL)
	if err != nil {
		return IntegrationView{}, fmt.Errorf("center: prepare private Center HTTPS: %w", err)
	}
	binding, _, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return IntegrationView{}, err
	}
	centerAliases, headscaleAliases, err := s.store.deploymentEndpointAliases(ctx)
	if err != nil {
		return IntegrationView{}, err
	}
	result, err := s.infrastructure.InstallHeadscale(ctx, deployapi.HeadscaleInstallRequest{
		CenterURL:               centerURL,
		HeadscaleURL:            input.URL,
		CenterAliases:           centerAliases,
		HeadscaleAliases:        headscaleAliases,
		PublicAddress:           binding.PublicAddress,
		GatewayBindAddress:      binding.BindAddress,
		CenterCertificatePEM:    centerCertificate.CertificatePEM,
		CenterCertificateKeyPEM: centerCertificate.PrivateKeyPEM,
	})
	if err != nil {
		return IntegrationView{}, err
	}
	value, err := s.store.ConfigureBuiltinHeadscale(ctx, result.Endpoint, result.APIKey)
	if err != nil {
		return IntegrationView{}, err
	}
	if err := s.store.queueAllGatewayStates(ctx); err != nil {
		return IntegrationView{}, err
	}
	if err := s.store.markBuiltinHeadscaleRuntime(ctx); err != nil {
		return IntegrationView{}, err
	}
	return value, nil
}

// ReconcileBuiltinHeadscale applies the current fixed runtime specification to
// an existing bundled installation without rotating its stored API key.
func (s *Server) ReconcileBuiltinHeadscale(ctx context.Context) (err error) {
	defer func() {
		if err == nil {
			s.startupReady.Store(true)
		}
	}()
	if s.infrastructure == nil {
		return nil
	}
	endpoint, runtime, configured, err := s.store.builtinHeadscaleRuntime(ctx)
	if err != nil || !configured || runtime == builtinHeadscaleRuntimeVersion {
		return err
	}
	network, err := s.store.CenterNetworkConfig(ctx)
	if err != nil {
		return err
	}
	centerCertificate, _, err := s.store.ensureSystemCenterCertificate(ctx, network.AgentConnectURL)
	if err != nil {
		return err
	}
	binding, _, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return err
	}
	centerAliases, headscaleAliases, err := s.store.deploymentEndpointAliases(ctx)
	if err != nil {
		return err
	}
	if err := s.infrastructure.ReconcileHeadscale(ctx, deployapi.HeadscaleInstallRequest{
		CenterURL:               network.AgentConnectURL,
		HeadscaleURL:            endpoint,
		CenterAliases:           centerAliases,
		HeadscaleAliases:        headscaleAliases,
		PublicAddress:           binding.PublicAddress,
		GatewayBindAddress:      binding.BindAddress,
		CenterCertificatePEM:    centerCertificate.CertificatePEM,
		CenterCertificateKeyPEM: centerCertificate.PrivateKeyPEM,
	}); err != nil {
		return err
	}
	if err := s.store.reconcileHeadscaleDNS(ctx); err != nil {
		return err
	}
	if err := s.store.queueAllGatewayStates(ctx); err != nil {
		return err
	}
	if err := s.store.removePublicCenterSetupDNS(ctx, network.AgentConnectURL); err != nil {
		return err
	}
	return s.store.markBuiltinHeadscaleRuntime(ctx)
}

func (s *Server) handleCreateHeadscaleJoin(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.CreateHeadscaleJoin(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}
