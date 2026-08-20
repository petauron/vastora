package center

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
)

type fakeBuiltinHeadscaleInstaller struct {
	endpoint string
	input    deployapi.HeadscaleInstallRequest
}

func (installer *fakeBuiltinHeadscaleInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: installer.endpoint, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"}, nil
}

func TestSetupInstallsBuiltinHeadscaleWithoutAcceptingAnAPIKey(t *testing.T) {
	headscale := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user" {
			t.Fatalf("unexpected Headscale verification path: %s", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"users": []any{}})
	}))
	defer headscale.Close()
	headscaleEndpoint := fmt.Sprintf("https://example.com:%d", headscale.Listener.Addr().(*net.TCPAddr).Port)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.headscaleHTTPClient = headscale.Client()
	if _, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{endpoint: headscaleEndpoint}
	server := NewServer(store, "", false).WithHeadscaleInstaller(installer)
	payload, _ := json.Marshal(InitialSetupInput{
		Site:      SiteInput{Name: "DMIT", Code: "dmit", Timezone: "Asia/Singapore"},
		Network:   CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "https://center.example.com"},
		Headscale: &HeadscaleInput{Mode: "builtin", URL: "https://headscale.example.com"},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSetupComplete(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", response.Code, response.Body.String())
	}
	if installer.input.CenterURL != "https://center.example.com" || installer.input.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected deployment input: %#v", installer.input)
	}
	integration, err := store.Integration(context.Background(), "headscale")
	if err != nil {
		t.Fatal(err)
	}
	if integration.Mode != "builtin" || integration.Endpoint != headscaleEndpoint || !integration.SecretSet {
		t.Fatalf("unexpected saved integration: %#v", integration)
	}
	client, err := store.headscale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.verify(context.Background()); err != nil {
		t.Fatalf("stored built-in Headscale did not use the local gateway: %v", err)
	}
	if _, err := store.ConfigureHeadscale(context.Background(), HeadscaleInput{Mode: "builtin", URL: headscale.URL, APIKey: "user-supplied-key-is-not-accepted"}); err == nil {
		t.Fatal("Store accepted a user-supplied built-in Headscale key")
	}
}
