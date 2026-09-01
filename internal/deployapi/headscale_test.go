package deployapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		switch {
		case request.Method == http.MethodPost && (request.URL.Path == "/v1/headscale/install" || request.URL.Path == "/v1/headscale/reconcile"):
			var input HeadscaleInstallRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.CenterURL != "https://center.example.com" || input.HeadscaleURL != "https://headscale.example.com" {
				t.Fatalf("unexpected input: %#v", input)
			}
			if request.URL.Path == "/v1/headscale/install" {
				_ = json.NewEncoder(writer).Encode(HeadscaleInstallResult{Endpoint: input.HeadscaleURL, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz", APIKeyID: 1, APIKeyPrefix: "abcdefghijkl", APIKeyExpiresAt: time.Now().Add(365 * 24 * time.Hour)})
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ready"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/public-entry/probes":
			var input PublicEntryProbeRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.BindAddress != "10.0.0.157" {
				t.Fatalf("unexpected probe input: %#v", input)
			}
			_ = json.NewEncoder(writer).Encode(PublicEntryProbe{ID: "probe-id", Challenge: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ", Ports: []int{80, 443}, ExpiresAt: "2026-08-25T00:00:30Z"})
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/public-entry/probes/probe-id":
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "stopped"})
		case request.Method == http.MethodPut && request.URL.Path == "/v1/center/remote-access":
			var input CenterRemoteAccessRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if !input.Enabled || input.Token != "cloudflare-tunnel-token-value" {
				t.Fatalf("unexpected remote access input: %#v", input)
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ready"})
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	client, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InstallHeadscale(context.Background(), HeadscaleInstallRequest{
		CenterURL: "https://center.example.com", HeadscaleURL: "https://headscale.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.APIKey != "hskey-api-abcdefghijklmnopqrstuvwxyz" {
		t.Fatal("deployment result was not returned")
	}
	if err := client.ReconcileHeadscale(context.Background(), HeadscaleInstallRequest{
		CenterURL: "https://center.example.com", HeadscaleURL: "https://headscale.example.com",
	}); err != nil {
		t.Fatal(err)
	}
	probe, err := client.StartPublicEntryProbe(context.Background(), PublicEntryProbeRequest{BindAddress: "10.0.0.157"})
	if err != nil {
		t.Fatal(err)
	}
	if probe.ID != "probe-id" || probe.Ports[0] != 80 || probe.Ports[1] != 443 {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
	if err := client.StopPublicEntryProbe(context.Background(), probe.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.ApplyCenterRemoteAccess(context.Background(), CenterRemoteAccessRequest{Enabled: true, Token: "cloudflare-tunnel-token-value"}); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeHeadscaleDNS(t *testing.T) {
	policy, resolvers, err := NormalizeHeadscaleDNS("", nil)
	if err != nil || policy != HeadscaleDNSPolicySystem || len(resolvers) != 0 {
		t.Fatalf("default Headscale DNS = %q %#v, err = %v", policy, resolvers, err)
	}
	policy, resolvers, err = NormalizeHeadscaleDNS("custom", []string{" 2001:0db8::1 ", "2001:db8::1", "192.0.2.53"})
	if err != nil || policy != HeadscaleDNSPolicyCustom || len(resolvers) != 2 || resolvers[0] != "2001:db8::1" || resolvers[1] != "192.0.2.53" {
		t.Fatalf("custom Headscale DNS = %q %#v, err = %v", policy, resolvers, err)
	}
	for _, invalid := range [][]string{nil, []string{"resolver.example.com"}, []string{"0.0.0.0"}, []string{"ff02::1"}} {
		if _, _, err := NormalizeHeadscaleDNS("custom", invalid); err == nil {
			t.Fatalf("invalid resolvers were accepted: %#v", invalid)
		}
	}
	if _, _, err := NormalizeHeadscaleDNS("system", []string{"1.1.1.1"}); err == nil {
		t.Fatal("system DNS policy accepted custom resolvers")
	}
}
