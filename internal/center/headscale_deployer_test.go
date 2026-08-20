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
	endpoint       string
	input          deployapi.HeadscaleInstallRequest
	reconcileInput deployapi.HeadscaleInstallRequest
}

func (installer *fakeBuiltinHeadscaleInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: installer.endpoint, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"}, nil
}

func (installer *fakeBuiltinHeadscaleInstaller) ReconcileHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) error {
	installer.reconcileInput = input
	return nil
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
	_, runtime, configured, err := store.builtinHeadscaleRuntime(context.Background())
	if err != nil || !configured || runtime != builtinHeadscaleRuntimeVersion {
		t.Fatalf("built-in runtime was not marked current: configured=%v runtime=%q err=%v", configured, runtime, err)
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

func TestReconcileBuiltinHeadscaleAppliesAnOlderRuntimeOnce(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)`, agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at)
		VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithHeadscaleInstaller(installer)
	if err := server.ReconcileBuiltinHeadscale(ctx); err != nil {
		t.Fatal(err)
	}
	if installer.reconcileInput.CenterURL != "https://center.example.com" || installer.reconcileInput.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected reconciliation input: %#v", installer.reconcileInput)
	}
	installer.reconcileInput = deployapi.HeadscaleInstallRequest{}
	if err := server.ReconcileBuiltinHeadscale(ctx); err != nil {
		t.Fatal(err)
	}
	if installer.reconcileInput != (deployapi.HeadscaleInstallRequest{}) {
		t.Fatal("current built-in runtime was reconciled again")
	}
}
