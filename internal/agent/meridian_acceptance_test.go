package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func TestMeridianAcceptanceContainerIsolation(t *testing.T) {
	options := meridianAcceptanceOptions("owned", "private-network")
	config, host := options.Config, options.HostConfig
	port := network.MustParsePort("1080/tcp")
	if config.Image != xrayWorkerImageReference || config.User != "1000:1000" || !config.OpenStdin || !config.StdinOnce || !config.AttachStdin || strings.Join(config.Cmd, " ") != "run -c stdin:" || len(config.Env) != 0 {
		t.Fatal("unbounded or secret-bearing client launch")
	}
	bindings := host.PortBindings[port]
	if host.NetworkMode != "private-network" || len(bindings) != 1 || bindings[0].HostIP != netip.MustParseAddr("127.0.0.1") || bindings[0].HostPort != "" || len(host.Binds) != 0 || len(host.Mounts) != 0 {
		t.Fatal("client is exposed or uses host credential files")
	}
	if !host.ReadonlyRootfs || host.NanoCPUs != 1000000000 || host.Memory != 128<<20 || host.MemorySwap != host.Memory || host.LogConfig.Type != "none" || len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || *host.PidsLimit != 32 {
		t.Fatal("client resource/security bounds missing")
	}
}

func TestMeridianAcceptanceCleanupRequiresOwnershipAndSuccessfulRemoval(t *testing.T) {
	for _, mode := range []string{"owned", "foreign", "remove-failed", "missing"} {
		t.Run(mode, func(t *testing.T) {
			removedContainer, removedNetwork := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("API-Version", "1.55")
				case strings.HasSuffix(r.URL.Path, "/containers/owned/json"):
					if mode == "missing" {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{"message":"not found"}`))
						return
					}
					owner := "owned"
					if mode == "foreign" {
						owner = "different-operation"
					}
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "immutable-container", Config: &container.Config{Labels: map[string]string{acceptanceOwnerLabel: owner}}})
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/immutable-container"):
					removedContainer = true
					if mode == "remove-failed" {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"message":"failed"}`))
						return
					}
					w.WriteHeader(204)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks/owned"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "immutable-network", "Labels": map[string]string{acceptanceOwnerLabel: "owned"}})
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/networks/immutable-network"):
					removedNetwork = true
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected Docker action %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			err = cleanupMeridianAcceptance(context.Background(), docker, "owned")
			wantSuccess := mode == "owned" || mode == "missing"
			if (err == nil) != wantSuccess || removedNetwork != wantSuccess || removedContainer != (mode == "owned" || mode == "remove-failed") {
				t.Fatalf("cleanup outcome: %v container=%v network=%v", err, removedContainer, removedNetwork)
			}
		})
	}
}
