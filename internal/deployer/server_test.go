package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type fakeInstaller struct {
	input          deployapi.HeadscaleInstallRequest
	reconcileInput deployapi.HeadscaleInstallRequest
}

type blockingInstaller struct {
	started chan string
	release chan struct{}
}

func (installer *blockingInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.started <- "install"
	<-installer.release
	return deployapi.HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz", APIKeyID: 1, APIKeyPrefix: "abcdefghijkl", APIKeyExpiresAt: time.Now().Add(365 * 24 * time.Hour)}, nil
}

func (installer *blockingInstaller) ReconcileHeadscale(_ context.Context, _ deployapi.HeadscaleInstallRequest) error {
	installer.started <- "reconcile"
	return nil
}

type fakePublicEntryProber struct {
	input     deployapi.PublicEntryProbeRequest
	stoppedID string
}

type fakeCenterRemoteAccessManager struct {
	input deployapi.CenterRemoteAccessRequest
}

func (manager *fakeCenterRemoteAccessManager) ApplyCenterRemoteAccess(_ context.Context, input deployapi.CenterRemoteAccessRequest) error {
	manager.input = input
	return nil
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
	return deployapi.HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz", APIKeyID: 1, APIKeyPrefix: "abcdefghijkl", APIKeyExpiresAt: time.Now().Add(365 * 24 * time.Hour)}, nil
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

func TestServerExposesOnlyTheFixedCenterRemoteAccessOperation(t *testing.T) {
	manager := &fakeCenterRemoteAccessManager{}
	handler := NewServer(&fakeInstaller{}).WithCenterRemoteAccessManager(manager).Handler()
	payload, _ := json.Marshal(deployapi.CenterRemoteAccessRequest{Enabled: true, Token: "cloudflare-tunnel-token-value"})
	request := httptest.NewRequest(http.MethodPut, "/v1/center/remote-access", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !manager.input.Enabled || manager.input.Token != "cloudflare-tunnel-token-value" {
		t.Fatalf("unexpected remote access response %d %s input=%#v", response.Code, response.Body.String(), manager.input)
	}
}

func TestServerSerializesHeadscaleRuntimeOperations(t *testing.T) {
	installer := &blockingInstaller{started: make(chan string, 2), release: make(chan struct{})}
	handler := NewServer(installer).Handler()
	payload, _ := json.Marshal(deployapi.HeadscaleInstallRequest{CenterURL: "https://center.example.com", HeadscaleURL: "https://headscale.example.com"})
	request := func(path string) *http.Request {
		value := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		value.Header.Set("Content-Type", "application/json")
		return value
	}
	installed := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request("/v1/headscale/install"))
		close(installed)
	}()
	select {
	case operation := <-installer.started:
		if operation != "install" {
			t.Fatalf("unexpected first operation %q", operation)
		}
	case <-time.After(time.Second):
		t.Fatal("Headscale installation did not start")
	}
	reconciled := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request("/v1/headscale/reconcile"))
		close(reconciled)
	}()
	select {
	case operation := <-installer.started:
		t.Fatalf("Headscale operation %q overlapped installation", operation)
	case <-time.After(100 * time.Millisecond):
	}
	close(installer.release)
	select {
	case <-installed:
	case <-time.After(time.Second):
		t.Fatal("Headscale installation did not complete")
	}
	select {
	case operation := <-installer.started:
		if operation != "reconcile" {
			t.Fatalf("unexpected second operation %q", operation)
		}
	case <-time.After(time.Second):
		t.Fatal("Headscale reconciliation did not start after installation")
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("Headscale reconciliation did not complete")
	}
}
