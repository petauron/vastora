package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
)

type fakeInstaller struct {
	input          deployapi.HeadscaleInstallRequest
	reconcileInput deployapi.HeadscaleInstallRequest
}

type fakePublicEntryProber struct {
	input     deployapi.PublicEntryProbeRequest
	stoppedID string
}

func (prober *fakePublicEntryProber) StartPublicEntryProbe(_ context.Context, input deployapi.PublicEntryProbeRequest) (deployapi.PublicEntryProbe, error) {
	prober.input = input
	return deployapi.PublicEntryProbe{ID: "probe-id", Challenge: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ", Ports: []int{80, 443}, ExpiresAt: "2026-08-25T00:00:30Z"}, nil
}

func (prober *fakePublicEntryProber) StopPublicEntryProbe(_ context.Context, id string) error {
	prober.stoppedID = id
	return nil
}

func (installer *fakeInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"}, nil
}

func (installer *fakeInstaller) ReconcileHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) error {
	installer.reconcileInput = input
	return nil
}

func TestServerExposesOnlyTheFixedHeadscaleOperation(t *testing.T) {
	installer := &fakeInstaller{}
	prober := &fakePublicEntryProber{}
	server := NewServer(installer)
	server.publicEntryProber = prober
	handler := server.Handler()
	payload, _ := json.Marshal(deployapi.HeadscaleInstallRequest{CenterURL: "https://center.example.com", HeadscaleURL: "https://headscale.example.com"})
	request := httptest.NewRequest(http.MethodPost, "/v1/headscale/install", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || installer.input.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected response %d %s", response.Code, response.Body.String())
	}
	reconcile := httptest.NewRequest(http.MethodPost, "/v1/headscale/reconcile", bytes.NewReader(payload))
	reconcile.Header.Set("Content-Type", "application/json")
	reconciled := httptest.NewRecorder()
	handler.ServeHTTP(reconciled, reconcile)
	if reconciled.Code != http.StatusOK || installer.reconcileInput.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected reconciliation response %d %s", reconciled.Code, reconciled.Body.String())
	}
	probePayload, _ := json.Marshal(deployapi.PublicEntryProbeRequest{BindAddress: "10.0.0.157"})
	probeRequest := httptest.NewRequest(http.MethodPost, "/v1/public-entry/probes", bytes.NewReader(probePayload))
	probeRequest.Header.Set("Content-Type", "application/json")
	probeResponse := httptest.NewRecorder()
	handler.ServeHTTP(probeResponse, probeRequest)
	if probeResponse.Code != http.StatusOK || prober.input.BindAddress != "10.0.0.157" {
		t.Fatalf("unexpected probe response %d %s", probeResponse.Code, probeResponse.Body.String())
	}
	stopResponse := httptest.NewRecorder()
	handler.ServeHTTP(stopResponse, httptest.NewRequest(http.MethodDelete, "/v1/public-entry/probes/probe-id", nil))
	if stopResponse.Code != http.StatusOK || prober.stoppedID != "probe-id" {
		t.Fatalf("unexpected stop response %d %s", stopResponse.Code, stopResponse.Body.String())
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/v1/docker/run", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("arbitrary Docker route is exposed: %d", unknown.Code)
	}
}
