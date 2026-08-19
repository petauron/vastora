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
	input deployapi.HeadscaleInstallRequest
}

func (installer *fakeInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"}, nil
}

func TestServerExposesOnlyTheFixedHeadscaleOperation(t *testing.T) {
	installer := &fakeInstaller{}
	handler := NewServer(installer).Handler()
	payload, _ := json.Marshal(deployapi.HeadscaleInstallRequest{CenterURL: "https://center.example.com", HeadscaleURL: "https://headscale.example.com"})
	request := httptest.NewRequest(http.MethodPost, "/v1/headscale/install", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || installer.input.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected response %d %s", response.Code, response.Body.String())
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/v1/docker/run", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("arbitrary Docker route is exposed: %d", unknown.Code)
	}
}
