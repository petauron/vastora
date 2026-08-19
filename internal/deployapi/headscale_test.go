package deployapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestClientUsesOnlyTheConfiguredUnixSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "vastora-deployapi-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "deployer.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/headscale/install" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		var input HeadscaleInstallRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.CenterURL != "https://center.example.com:8443" || input.HeadscaleURL != "https://headscale.example.com:8443" {
			t.Fatalf("unexpected input: %#v", input)
		}
		_ = json.NewEncoder(writer).Encode(HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"})
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	client, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InstallHeadscale(context.Background(), HeadscaleInstallRequest{
		CenterURL: "https://center.example.com:8443", HeadscaleURL: "https://headscale.example.com:8443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.APIKey != "hskey-api-abcdefghijklmnopqrstuvwxyz" {
		t.Fatal("deployment result was not returned")
	}
}
