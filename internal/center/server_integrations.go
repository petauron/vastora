package center

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
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

func (s *Server) handleRetireSystemEndpointAliases(writer http.ResponseWriter, request *http.Request) {
	if err := s.RetireSystemEndpointAliases(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.SystemDomain(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
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
	return s.configureHeadscaleOperation(ctx, input, centerURL, "")
}

func (s *Server) configureHeadscaleOperation(ctx context.Context, input HeadscaleInput, centerURL, operationID string) (IntegrationView, error) {
	if strings.TrimSpace(input.Mode) != "builtin" {
		return s.store.ConfigureHeadscale(ctx, input)
	}
	if s.infrastructure == nil {
		return IntegrationView{}, errors.New("center: this installation does not include the Headscale deployment helper")
	}
	if strings.TrimSpace(input.APIKey) != "" {
		return IntegrationView{}, errors.New("center: built-in Headscale creates its API key automatically")
	}
	dnsPolicy, dnsResolvers, err := deployapi.NormalizeHeadscaleDNS(input.DNSPolicy, input.DNSResolvers)
	if err != nil {
		return IntegrationView{}, fmt.Errorf("center: Headscale DNS: %w", err)
	}
	input.DNSPolicy = dnsPolicy
	input.DNSResolvers = dnsResolvers
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()

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
	centerPrivateBindAddress, err := s.store.coLocatedHeadscaleAddress(ctx)
	if err != nil {
		return IntegrationView{}, err
	}
	installRequest := deployapi.HeadscaleInstallRequest{
		CenterURL:                centerURL,
		HeadscaleURL:             input.URL,
		CenterAliases:            centerAliases,
		HeadscaleAliases:         headscaleAliases,
		PublicAddress:            binding.PublicAddress,
		GatewayBindAddress:       binding.BindAddress,
		CenterPrivateBindAddress: centerPrivateBindAddress,
		CenterCertificatePEM:     centerCertificate.CertificatePEM,
		CenterCertificateKeyPEM:  centerCertificate.PrivateKeyPEM,
		DNSPolicy:                input.DNSPolicy,
		DNSResolvers:             input.DNSResolvers,
	}
	if operationID == "" {
		encodedRequest, marshalErr := json.Marshal(installRequest)
		if marshalErr != nil {
			return IntegrationView{}, marshalErr
		}
		operationID = fmt.Sprintf("headscale-%x", sha256.Sum256(encodedRequest))
	}
	installRequest.OperationID = operationID
	result, err := s.infrastructure.InstallHeadscale(ctx, installRequest)
	if err != nil {
		return IntegrationView{}, err
	}
	value, err := s.store.ConfigureBuiltinHeadscale(ctx, result, input.DNSPolicy, input.DNSResolvers)
	if err != nil {
		return IntegrationView{}, err
	}
	if err := s.finalizeSetupHeadscale(ctx, "builtin", operationID); err != nil {
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
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()

	_, runtime, configured, err := s.store.builtinHeadscaleRuntime(ctx)
	if err != nil || !configured || runtime == builtinHeadscaleRuntimeVersion {
		return err
	}
	snapshot, configured, err := s.builtinHeadscaleReconcileSnapshot(ctx)
	if err != nil {
		return err
	}
	if !configured || snapshot.Runtime != runtime {
		return errors.New("center: bundled Headscale desired state changed before reconciliation")
	}
	if err := s.infrastructure.ReconcileHeadscale(ctx, snapshot.Request); err != nil {
		return err
	}
	latest, configured, err := s.builtinHeadscaleReconcileSnapshot(ctx)
	if err != nil {
		return err
	}
	if !configured || latest.Runtime != snapshot.Runtime || !reflect.DeepEqual(latest.Request, snapshot.Request) {
		return errors.New("center: bundled Headscale desired state changed during reconciliation")
	}
	if err := s.store.reconcileHeadscaleDNS(ctx); err != nil {
		return err
	}
	if err := s.store.removePublicCenterSetupDNS(ctx, snapshot.Request.CenterURL); err != nil {
		return err
	}
	return s.store.markBuiltinHeadscaleRuntime(ctx)
}

type builtinHeadscaleReconcileState struct {
	Runtime string
	Request deployapi.HeadscaleInstallRequest
}

func (s *Server) builtinHeadscaleReconcileSnapshot(ctx context.Context) (builtinHeadscaleReconcileState, bool, error) {
	endpoint, runtime, configured, err := s.store.builtinHeadscaleRuntime(ctx)
	if err != nil || !configured {
		return builtinHeadscaleReconcileState{}, configured, err
	}
	network, err := s.store.CenterNetworkConfig(ctx)
	if err != nil {
		return builtinHeadscaleReconcileState{}, false, err
	}
	centerCertificate, _, err := s.store.ensureSystemCenterCertificate(ctx, network.AgentConnectURL)
	if err != nil {
		return builtinHeadscaleReconcileState{}, false, err
	}
	binding, _, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return builtinHeadscaleReconcileState{}, false, err
	}
	centerAliases, headscaleAliases, err := s.store.deploymentEndpointAliases(ctx)
	if err != nil {
		return builtinHeadscaleReconcileState{}, false, err
	}
	dnsPolicy, dnsResolvers, err := s.store.builtinHeadscaleDNSConfig(ctx)
	if err != nil {
		return builtinHeadscaleReconcileState{}, false, err
	}
	return builtinHeadscaleReconcileState{
		Runtime: runtime,
		Request: deployapi.HeadscaleInstallRequest{
			CenterURL:          network.AgentConnectURL,
			HeadscaleURL:       endpoint,
			CenterAliases:      centerAliases,
			HeadscaleAliases:   headscaleAliases,
			PublicAddress:      binding.PublicAddress,
			GatewayBindAddress: binding.BindAddress,
			// Startup reconciliation must not trust a pre-restart tailnet address.
			// The co-located Agent queues the private listener after tailscale0 is
			// observed again; its control channel remains on the host-only port.
			CenterPrivateBindAddress: "",
			CenterCertificatePEM:     centerCertificate.CertificatePEM,
			CenterCertificateKeyPEM:  centerCertificate.PrivateKeyPEM,
			DNSPolicy:                dnsPolicy,
			DNSResolvers:             dnsResolvers,
		},
	}, true, nil
}

func (s *Server) handleCreateHeadscaleJoin(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.CreateHeadscaleJoin(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value)
}
